package core

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestContinuingWorkRemainsVisibleAfterItsAgentExits(t *testing.T) {
	world := newWorld(t)
	world.run(42, 0, "/dev/a", "claude", "lead", Idle)
	c := world.core
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.ObserveWork(b); err != nil {
		t.Fatal(err)
	}
	b, err = c.Board()
	if err != nil {
		t.Fatal(err)
	}
	var w *work.Scope
	for _, p := range b.Projects {
		if p.Work != nil {
			w = p.Work
			break
		}
	}
	if w == nil {
		t.Fatal("discovery did not attach durable work")
	}
	c.Procs = func() ([]proc.Proc, error) { return nil, nil }
	c.Harnesses = nil
	b, err = c.Board()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range b.Projects {
		if p.Work != nil && p.Work.ID == w.ID {
			found = true
			if !p.Shown() {
				t.Fatal("continuing work hidden after exit")
			}
		}
	}
	if !found {
		t.Fatal("agent exit deleted continuing work")
	}
}

func TestUnrelatedBriefUpdatesDoNotInvalidateAReservedJudgment(t *testing.T) {
	for _, revisePacket := range []bool{false, true} {
		t.Run(fmt.Sprint(revisePacket), func(t *testing.T) {
			s, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			c := &Core{Store: s}
			w, err := s.Work.Ensure("fixture", "repo", "Review")
			if err != nil {
				t.Fatal(err)
			}
			apply := func(e work.Event) {
				t.Helper()
				e.Work, e.Expected = w.ID, w.Revision
				w, err = s.Work.Commit(e)
				if err != nil {
					t.Fatal(err)
				}
			}
			apply(work.Event{Type: "observe", Review: work.Review{ID: "pr", Version: "head1", Order: 1}})
			apply(work.Event{Type: "prepare", Target: "pr", Review: work.Review{Version: "head1", Question: "Approve?", Recommendation: "gated rollout", Evidence: "rollback verified"}})
			viewed := w
			if revisePacket {
				apply(work.Event{Type: "prepare", Target: "pr", Review: work.Review{Version: "head1", Question: "Approve?", Recommendation: "full rollout", Evidence: "changed assessment"}})
			} else {
				apply(work.Event{Type: "brief", Brief: work.Brief{Assessment: "Another workstream finished", Context: 0}})
			}
			_, err = c.ChangeWork(viewed, work.Event{Type: "decide", Actor: "owner", Target: "pr", Text: "accept gated rollout"})
			if revisePacket && err == nil {
				t.Fatal("changed review packet accepted")
			}
			if !revisePacket && err != nil {
				t.Fatalf("unrelated assessment blocked a valid judgment: %v", err)
			}
		})
	}
}

func TestRemoteOutflowStaysQueuedUntilItsOriginAppliesIt(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := work.Scope{ID: "remote-work", Name: "Remote search", Revision: 4, Context: work.Context{Revision: 1, Text: "old guidance"}}
	if err = s.Work.AddPeer("worker-a"); err != nil {
		t.Fatal(err)
	}
	if err = s.Work.SavePeer(work.Peer{Host: "worker-a", Scopes: []work.Scope{scope}}); err != nil {
		t.Fatal(err)
	}
	c := &Core{Store: s, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	w := b.Projects[0].Work
	_, err = c.ChangeWork(*w, work.Event{Type: "context", Actor: "owner", Text: "new direction"})
	if err != nil {
		t.Fatal(err)
	}
	commands, err := s.Work.RemoteCommands("worker-a")
	if err != nil || len(commands) != 1 || commands[0].Work != "remote-work" {
		t.Fatalf("remote outflow lost: %+v %v", commands, err)
	}
	peer, _ := s.Work.Peers()
	if peer[0].Scopes[0].Context.Text != "old guidance" {
		t.Fatal("queued guidance was presented as applied")
	}
	if err = s.Work.SavePeer(work.Peer{Host: "worker-a", Error: "offline"}); err != nil {
		t.Fatal(err)
	}
	peer, _ = s.Work.Peers()
	if len(peer[0].Scopes) != 1 {
		t.Fatal("losing a host erased its captured context")
	}
}

func TestJudgmentChecksTheSourceAgainBeforeRecordingAcceptance(t *testing.T) {
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &Core{Store: st}
	w, err := st.Work.Ensure("fixture", "/src/query/.git", "Search")
	if err != nil {
		t.Fatal(err)
	}
	apply := func(e work.Event) {
		t.Helper()
		e.Work, e.Expected = w.ID, w.Revision
		w, err = st.Work.Commit(e)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(work.Event{Type: "watch", Source: work.Source{Kind: "github", URL: "https://github.com/example/query/pull/241"}})
	apply(work.Event{Type: "observe", Review: work.Review{ID: "pr", URL: "https://github.com/example/query/pull/241", Version: "head1", Order: 1}})
	apply(work.Event{Type: "prepare", Target: "pr", Review: work.Review{Version: "head1", Question: "Approve?", Recommendation: "yes", Evidence: "verified"}})
	c.CheckReview = func(source work.Source) (work.Review, error) {
		return work.Review{ID: "pr", URL: source.URL, Version: "head2", Order: 2}, nil
	}
	if _, err = c.ChangeWork(w, work.Event{Type: "decide", Actor: "owner", Target: "pr", Text: "approve"}); err == nil {
		t.Fatal("accepted a changed source")
	}
	current, _ := st.Work.Get(w.ID)
	if current.Reviews["pr"].State != "incoming" || current.Reviews["pr"].Version != "head2" {
		t.Fatal("new evidence was not retained for preparation")
	}
}

func TestAProjectMessageSurvivesAConcurrentContextUpdate(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &Core{Store: s}
	w, err := s.Work.Ensure("fixture", "repo", "Compatibility")
	if err != nil {
		t.Fatal(err)
	}
	w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "manager", Target: "manager"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "context", Text: "Keep both API and SDK"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.ChangeWork(w, work.Event{Type: "message", Target: w.Manager, Actor: "owner", Text: "Retain the SDK example"})
	if err != nil {
		t.Fatalf("owner message lost after direction changed: %v", err)
	}
	updates, err := s.Work.Inbox(w.Manager)
	if err != nil || !strings.Contains(strings.Join(updates, "\n"), "Retain the SDK example") {
		t.Fatalf("message missing from inbox: %v %v", updates, err)
	}
}

func TestExplicitKillRetiresSessionMissionAndKeepsItsBrief(t *testing.T) {
	for _, tc := range []struct {
		name                               string
		delegated, revived, pending, stale bool
	}{
		{name: "stopped mission"}, {name: "delegated commitment", delegated: true}, {name: "session resumed", revived: true}, {name: "pending merge", pending: true}, {name: "late commit of old observation", stale: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			world := newWorld(t)
			world.run(42, 0, "/dev/a", "claude", "lead", Idle)
			c := world.core
			b, err := c.Board()
			if err != nil {
				t.Fatal(err)
			}
			if err = c.ObserveWork(b); err != nil {
				t.Fatal(err)
			}
			all, _ := c.Store.Work.List()
			if len(all) != 1 {
				t.Fatalf("work %d", len(all))
			}
			w := all[0]
			apply := func(e work.Event) {
				t.Helper()
				e.Work, e.Expected = w.ID, w.Revision
				w, err = c.Store.Work.Commit(e)
				if err != nil {
					t.Fatal(err)
				}
			}
			apply(work.Event{Type: "brief", Brief: work.Brief{Assessment: "Keep caller compatibility"}})
			if tc.delegated {
				a, err := c.Store.Work.CreateAgent("Chief", "chief", "", "Coordinate caller migration", "Inspect and coordinate", work.Execution{Harness: "codex", Directory: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				apply(work.Event{Type: "owner", Actor: "owner", Target: a.ID})
			}
			if tc.pending {
				url := "https://github.com/example/query/pull/42"
				apply(work.Event{Type: "merge-status", Review: work.Review{ID: work.MergeReviewID(url), URL: url, Version: "head1", Action: "merge", State: work.Ready}})
			}
			at := c.now().Add(time.Second)
			if err = c.Store.RecordKill("lead", at); err != nil {
				t.Fatal(err)
			}
			if tc.revived {
				c.Now = func() time.Time { return at.Add(time.Second) }
				if err = c.ObserveWork(world.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			if tc.stale {
				apply(work.Event{Type: "execution", At: at.Add(time.Second), Target: "lead", Observation: work.ExecutionObservation{State: "busy"}})
			}
			c.Procs = func() ([]proc.Proc, error) { return nil, nil }
			c.Harnesses = nil
			b, err = c.Board()
			if err != nil {
				t.Fatal(err)
			}
			if err = c.ObserveWork(b); err != nil {
				t.Fatal(err)
			}
			w, _ = c.Store.Work.Get(w.ID)
			wantArchived := !tc.delegated && !tc.revived
			if (w.State == "archived") != wantArchived || w.Brief.Assessment != "Keep caller compatibility" {
				t.Fatalf("mission %+v", w)
			}
			b, err = c.Board()
			if err != nil {
				t.Fatal(err)
			}
			for _, p := range b.Projects {
				if p.Work != nil && p.Work.ID == w.ID && p.Shown() != (tc.delegated || tc.revived) {
					t.Fatalf("visibility %+v", p)
				}
			}
			if tc.pending && !w.Reviews[work.MergeReviewID("https://github.com/example/query/pull/42")].Pending() {
				t.Fatal("archiving the session removed its owner merge obligation")
			}
		})
	}
}
