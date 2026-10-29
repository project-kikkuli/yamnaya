package core

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestChiefsCoordinateOutsideTheirReportsRepositories(t *testing.T) {
	for _, reference := range []string{"", "/repo/reference"} {
		t.Run(reference, func(t *testing.T) {
			w := newOrgWorld(t)
			_, first := w.goal(t, "ship service", "/repo/service")
			_, second := w.goal(t, "ship client", "/repo/client")
			chief, err := w.core.NewChief("platform", "coordinate both", reference, "", []string{first.ID, second.ID})
			if err != nil {
				t.Fatal(err)
			}
			expected := filepath.Join(w.core.StateDir, "chiefs", chief.ID, "workspace")
			launch := w.launches.starts[len(w.launches.starts)-1]
			if chief.Directory != expected || launch.Directory != expected || launch.Repo != reference {
				t.Fatalf("chief launch: %+v, %+v", chief, launch)
			}
			item, err := w.core.AddOwnerItem(Item{Kind: KindChore, Session: chief.Session, Question: "review cross-repo plan"})
			if err != nil || ScopeName(item.Scope) != chief.ID {
				t.Fatalf("chief acquired a repo identity: %+v, %v", item, err)
			}
			org, _ := w.core.LoadOrg()
			for _, report := range []Manager{first, second} {
				if org.Managers[report.ID].Repo != report.Repo || org.Managers[report.ID].ParentID != chief.ID {
					t.Fatalf("report moved: %+v", org.Managers[report.ID])
				}
			}
			if !strings.Contains(launch.Prompt, "not a repository") {
				t.Fatal("chief has no workspace explanation")
			}
			w.stop(chief.Session)
			w.core.relaunch(w.board(t), org, chief)
			if w.launches.starts[len(w.launches.starts)-1].Directory != expected {
				t.Fatal("relaunch lost workspace")
			}
		})
	}
}

func TestConsolidatingARefileRestoresTheOriginalGoalRepositoryAndAnswerRoute(t *testing.T) {
	for _, prior := range []string{ItemOpen, ItemAnswered, ItemDismissed} {
		t.Run(prior, func(t *testing.T) {
			w := newOrgWorld(t)
			goal, manager := w.goal(t, "workplanes", "/repo/vibe-apps")
			chief := w.chief(t, "platform", manager.ID)
			original := w.ask(t, manager.Session, "build it inside workstations?")
			if prior != ItemOpen {
				if _, err := w.core.Store.Transition(original.Key(), prior, "refiled to chief"); err != nil {
					t.Fatal(err)
				}
			}
			copyItem := w.ask(t, chief.Session, "build workplanes inside workstations?")
			copyItem.Evidence = []string{"https://example.com/plan"}
			if err := w.core.Store.prepare(copyItem); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Consolidate(copyItem.Key(), original.Key(), "chief refiled the source request")
			if err != nil {
				t.Fatal(err)
			}
			if got.State != ItemOpen || got.Recipient != Owner || got.ID != original.ID || got.Scope != original.Scope || got.Session != original.Session || got.Dossier.Goal != goal.ID || !got.CreatedAt.Equal(original.CreatedAt) || len(got.Evidence) != 1 {
				t.Fatalf("source changed: %+v", got)
			}
			old, _ := w.core.Store.Item(copyItem.Key())
			if old.State != ItemDismissed || !strings.Contains(old.Reason, original.Qualified()) || len(old.History) < 2 {
				t.Fatalf("copy disappeared: %+v", old)
			}
			if _, _, err := w.core.Answer(w.board(t), got.Key(), "choose inside"); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(strings.Join(w.handedTo(manager.Session), "\n"), "choose inside") {
				t.Fatal("answer did not reach original manager")
			}
			if _, err := w.core.Consolidate(copyItem.Key(), original.Key(), "again"); err == nil {
				t.Fatal("closed copy was reused")
			}
		})
	}
}

func TestConsolidationRejectsUnrelatedOrUnpreparedCopiesWithoutChangingEither(t *testing.T) {
	for _, invalid := range []string{"same", "kind", "closed", "reason", "thin"} {
		t.Run(invalid, func(t *testing.T) {
			w := newOrgWorld(t)
			original := w.ask(t, "source", "choose?")
			copyItem := w.ask(t, "chief", "refiled choose?")
			why := "explicit correction"
			switch invalid {
			case "same":
				copyItem = original
			case "kind":
				w.core.Store.db.Exec(`UPDATE ym_items SET kind='chore' WHERE seq=?`, copyItem.Seq)
			case "closed":
				w.core.Store.Transition(copyItem.Key(), ItemAnswered, "done")
			case "reason":
				why = ""
			case "thin":
				w.core.Store.db.Exec(`UPDATE ym_items SET rec='', options='[]' WHERE seq IN (?,?)`, copyItem.Seq, original.Seq)
			}
			if _, err := w.core.Consolidate(copyItem.Key(), original.Key(), why); err == nil {
				t.Fatal("invalid consolidation accepted")
			}
			source, _ := w.core.Store.Item(original.Key())
			if source.State != ItemOpen || len(source.History) != 1 {
				t.Fatalf("source changed: %+v", source)
			}
		})
	}
}
