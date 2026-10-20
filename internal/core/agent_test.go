package core

import (
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestChiefDefaultsAttachNewSessionsWithoutInventingManagers(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &Core{Store: s}
	makeBoard := func(id, repo string) Board {
		return Board{Projects: []Project{{Key: id, Name: id, Repo: repo, Sessions: []Live{{Session: Session{ID: id, Harness: "codex", Cwd: "/src/query"}}}}}}
	}
	if err = c.ObserveWork(makeBoard("existing", "query")); err != nil {
		t.Fatal(err)
	}
	global, err := s.Work.CreateAgent("General", "chief", "", "Carry priorities", "Inspect and coordinate", work.Execution{Harness: "codex", Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := s.Work.CreateAgent("Query", "chief", "query", "Carry repository context", "Inspect and coordinate", work.Execution{Harness: "codex", Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []work.Rule{{Agent: global.ID}, {Agent: scoped.ID, Repo: "query"}} {
		if err = s.Work.SetRule(rule.Agent, rule.Repo, false); err != nil {
			t.Fatal(err)
		}
	}
	for _, b := range []Board{makeBoard("existing", "query"), makeBoard("new-query", "query"), makeBoard("new-other", "other")} {
		if err = c.ObserveWork(b); err != nil {
			t.Fatal(err)
		}
	}
	links, err := s.Work.Attachments()
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 {
		t.Fatalf("expected 2 general and 1 repository attachment: %+v", links)
	}
	for _, id := range []string{"existing", "new-query", "new-other"} {
		w, err := s.Work.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if w.Manager != "" || w.Owner != "" {
			t.Fatal("discovery invented accountable management")
		}
		for _, link := range links {
			if id == "existing" && link.Work == w.ID {
				t.Fatal("new-session rule retroactively attached an existing session")
			}
		}
	}
	old, err := s.Work.Find("existing")
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Work.Attach(global.ID, old.ID, "Explicitly include existing work"); err != nil {
		t.Fatal(err)
	}
	if err = s.Work.SetRule(global.ID, "", true); err != nil {
		t.Fatal(err)
	}
	links, _ = s.Work.Attachments()
	if len(links) != 4 {
		t.Fatal("removing a default detached existing work")
	}
}

func TestAgentSpendSurvivesExecutionReplacementWithoutDoubleCountingLiveSessions(t *testing.T) {
	for _, live := range []bool{false, true} {
		t.Run(map[bool]string{false: "background", true: "live"}[live], func(t *testing.T) {
			b := Board{Fleet: Fleet{Ready: true, Sessions: map[string]SessionSpend{"previous": {Total: Spend{USD: 9}}, "current": {Total: Spend{USD: 3}}}}, Agents: []work.Agent{{ID: "manager", Workspace: "brief", Session: "current", Sessions: []string{"previous", "current"}, Running: true}}, Projects: []Project{{Work: &work.Scope{ID: "brief", Kind: "agent"}}}}
			if live {
				b.Projects[0].Sessions = []Live{{Session: Session{ID: "current", Status: Busy}}}
				b.Projects[0].Spend = Spend{USD: 3}
			}
			attachAgentUsage(&b)
			if b.Projects[0].Spend.USD != 12 {
				t.Fatalf("execution replacement lost or duplicated spend: %+v", b.Projects[0].Spend)
			}
			if !live && b.Projects[0].Status != Busy {
				t.Fatal("background execution status disappeared")
			}
		})
	}
}

func TestChiefReceivesNativeCompletionAndExitWithoutSelfWakeLoops(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &Core{Store: s}
	a, err := s.Work.CreateAgent("General", "chief", "", "Carry context", "Inspect and coordinate", work.Execution{Harness: "codex", Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	settle := func() {
		t.Helper()
		x, err := s.Work.AgentSnapshot(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Pending) == 0 {
			return
		}
		if err = s.Work.Checkpoint(a.ID, "fixture", work.Checkpoint{Brief: work.Brief{Assessment: "Watching native work", Context: x.Workspace.Context.Revision}, Inputs: x.Pending, Next: "Investigate changed execution", WaitFor: "Native results or owner direction"}); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.Work.SetRule(a.ID, "", false); err != nil {
		t.Fatal(err)
	}
	settle()
	session := Live{Session: Session{ID: "worker", Status: Busy, Cwd: "/src/query", Harness: "codex"}}
	b := Board{Projects: []Project{{Key: "worker", Name: "Compatibility", Repo: "query", Sessions: []Live{session}}}}
	if err = c.ObserveWork(b); err != nil {
		t.Fatal(err)
	}
	settle()
	b.Projects[0].Sessions[0].Status = Idle
	b.Projects[0].Sessions[0].Result = "Implementation ready; caller fixtures pass"
	if err = c.ObserveWork(b); err != nil {
		t.Fatal(err)
	}
	x, err := s.Work.AgentSnapshot(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(x.Pending) != 1 {
		t.Fatal("native result did not wake its chief")
	}
	settle()
	if err = c.ObserveWork(Board{}); err != nil {
		t.Fatal(err)
	}
	x, _ = s.Work.AgentSnapshot(a.ID)
	if len(x.Pending) != 1 {
		t.Fatal("native exit disappeared from management")
	}
	settle()
	if err = c.ObserveWork(Board{}); err != nil {
		t.Fatal(err)
	}
	x, _ = s.Work.AgentSnapshot(a.ID)
	if len(x.Pending) != 0 {
		t.Fatal("unchanged exit repeatedly woke chief")
	}
	a.Session = "chief-native"
	if err = s.Work.SaveAgent(a); err != nil {
		t.Fatal(err)
	}
	b = Board{Projects: []Project{{Key: a.Session, Name: a.Name, Sessions: []Live{{Session: Session{ID: a.Session, Status: Busy, Harness: "codex"}}}}}}
	if err = c.ObserveWork(b); err != nil {
		t.Fatal(err)
	}
	x, _ = s.Work.AgentSnapshot(a.ID)
	if len(x.Pending) != 0 {
		t.Fatal("chief activity woke itself")
	}
	if err = c.ObserveWork(Board{}); err != nil {
		t.Fatal(err)
	}
	x, _ = s.Work.AgentSnapshot(a.ID)
	if len(x.Pending) != 0 {
		t.Fatal("chief exit woke itself")
	}
}
