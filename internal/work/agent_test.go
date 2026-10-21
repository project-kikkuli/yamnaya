package work_test

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func agent(t *testing.T, s *core.Store, name string) work.Agent {
	t.Helper()
	a, err := s.Work.CreateAgent(name, "chief", "", "Coordinate compatibility work", "Inspect, delegate, and verify; reserve publication for the owner", work.Execution{Harness: "codex", Directory: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func checkpoint(t *testing.T, s *core.Store, a work.Agent) work.Checkpoint {
	t.Helper()
	x, err := s.Work.AgentSnapshot(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	return work.Checkpoint{Brief: work.Brief{Assessment: "Compatibility is being verified", Context: x.Workspace.Context.Revision}, Inputs: x.Pending, WaitFor: "Owner direction or a registered source change", Next: "Investigate the next consequential change"}
}
func settle(t *testing.T, s *core.Store, a work.Agent) {
	t.Helper()
	if err := s.Work.Checkpoint(a.ID, "fixture", checkpoint(t, s, a)); err != nil {
		t.Fatal(err)
	}
}

func TestManagementAcknowledgesIncorporatedWorkRatherThanReceipts(t *testing.T) {
	for _, action := range []string{"receipt", "empty checkpoint", "obsolete context", "future input", "valid checkpoint"} {
		t.Run(action, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym.db")
			s := open(t, path)
			a := agent(t, s, "General")
			w, err := s.Work.Get(a.Workspace)
			if err != nil {
				t.Fatal(err)
			}
			c := checkpoint(t, s, a)
			switch action {
			case "receipt":
				if _, err = s.Work.Inbox(a.ID); err != nil {
					t.Fatal(err)
				}
			case "empty checkpoint":
				c.Next = ""
				c.WaitFor = ""
				err = s.Work.Checkpoint(a.ID, "bad", c)
			case "obsolete context":
				w = commit(t, s, w, work.Event{Type: "context", Actor: "owner", Text: "Preserve callers"})
				err = s.Work.Checkpoint(a.ID, "bad", c)
			case "future input":
				c.Inputs[w.ID] = w.Revision + 1
				err = s.Work.Checkpoint(a.ID, "bad", c)
			case "valid checkpoint":
				err = s.Work.Checkpoint(a.ID, "valid", c)
			}
			if action != "receipt" && action != "valid checkpoint" && err == nil {
				t.Fatal("unsubstantive checkpoint accepted")
			}
			s.Close()
			s = open(t, path)
			defer s.Close()
			x, err := s.Work.AgentSnapshot(a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if want := action != "valid checkpoint"; (len(x.Pending) > 0) != want {
				t.Fatalf("pending inputs lost or invented: %+v", x.Pending)
			}
			if action == "valid checkpoint" && (!x.Workspace.BriefCurrent() || x.Workspace.WaitFor == "" || x.Workspace.Adoptions[a.ID] != x.Workspace.Context.Revision) {
				t.Fatal("brief or next wake obligation lost")
			}
		})
	}
}

func TestDelegatorOwnsVerificationAndReassignmentRetiresOldOwnership(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	parent, worker, replacement := agent(t, s, "Coordinator"), agent(t, s, "Worker"), agent(t, s, "Successor")
	w, err := s.Work.Ensure("child", "repo", "Preserve v2 callers")
	if err != nil {
		t.Fatal(err)
	}
	w = commit(t, s, w, work.Event{Type: "parent", Target: parent.Workspace, Actor: parent.ID})
	w, err = s.Work.Assign(w.ID, worker.ID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []work.Agent{parent, worker, replacement} {
		settle(t, s, a)
	}
	w = commit(t, s, w, work.Event{Type: "result", Actor: worker.ID, Text: "go test proves v2 callers still resolve", Context: w.Context.Revision})
	if w.State != "verifying" {
		t.Fatal("claimed result was treated as completion")
	}
	x, err := s.Work.AgentSnapshot(parent.ID)
	if err != nil || x.Pending[w.ID] != w.Revision {
		t.Fatalf("delegator not woken: %+v %v", x.Pending, err)
	}
	for _, who := range []string{worker.ID, replacement.ID} {
		if _, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "verify", Actor: who, Context: w.Context.Revision, Text: "looks done"}); err == nil {
			t.Fatal("unaccountable verification accepted")
		}
	}
	w = commit(t, s, w, work.Event{Type: "context", Actor: "owner", Text: "Also preserve deeply nested callers"})
	if _, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "verify", Actor: parent.ID, Context: w.Context.Revision, Text: "old tests passed"}); err == nil {
		t.Fatal("obsolete result accepted")
	}
	w, err = s.Work.Assign(w.ID, replacement.ID, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	x, err = s.Work.AgentSnapshot(worker.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, pending := x.Pending[w.ID]; pending {
		t.Fatal("previous owner retained execution responsibility")
	}
	w = commit(t, s, w, work.Event{Type: "result", Actor: replacement.ID, Text: "Nested and direct caller tests pass", Context: w.Context.Revision})
	w = commit(t, s, w, work.Event{Type: "verify", Actor: parent.ID, Text: "Independently ran caller fixtures", Context: w.Context.Revision})
	if w.State != "completed" || w.VerifiedBy != parent.ID {
		t.Fatal("verified child did not complete")
	}
	if _, err = s.Work.Commit(work.Event{Work: parent.Workspace, Expected: 0, Type: "parent", Actor: "owner", Target: w.ID}); !errors.Is(err, work.ErrConflict) {
		t.Fatalf("concurrency not enforced: %v", err)
	}
	p, _ := s.Work.Get(parent.Workspace)
	if _, err = s.Work.Commit(work.Event{Work: p.ID, Expected: p.Revision, Type: "parent", Actor: "owner", Target: w.ID}); err == nil {
		t.Fatal("ownership cycle accepted")
	}
}

func TestChiefAttachmentsSurvivePeerLossAndCatchUpWithNewEvidence(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	a := agent(t, s, "General")
	settle(t, s, a)
	if err := s.Work.AddPeer("runner"); err != nil {
		t.Fatal(err)
	}
	p := work.Peer{Host: "runner", Fetched: time.Now(), Scopes: []work.Scope{{ID: "remote", Name: "Compatibility", Revision: 2}}}
	if err := s.Work.SavePeer(p); err != nil {
		t.Fatal(err)
	}
	if err := s.Work.Attach(a.ID, "runner/remote", "Coordinate compatibility across hosts"); err != nil {
		t.Fatal(err)
	}
	settle(t, s, a)
	p.Error = "connection unavailable"
	if err := s.Work.SavePeer(p); err != nil {
		t.Fatal(err)
	}
	p.Error = ""
	p.Scopes[0].Revision = 3
	p.Scopes[0].Brief.Assessment = "Migration ready"
	if err := s.Work.SavePeer(p); err != nil {
		t.Fatal(err)
	}
	x, err := s.Work.AgentSnapshot(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if x.Pending["runner/remote"] != 3 || len(x.Work) != 1 || x.Work[0].Brief.Assessment != "Migration ready" {
		t.Fatalf("remote catchup lost: %+v", x)
	}
	settle(t, s, a)
	if err = s.Work.SavePeer(p); err != nil {
		t.Fatal(err)
	}
	x, _ = s.Work.AgentSnapshot(a.ID)
	if len(x.Pending) != 0 {
		t.Fatal("unchanged peer snapshot repeatedly woke chief")
	}
}

func TestThousandWorkstreamsDrainInBoundedManagementBatches(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	a := agent(t, s, "General")
	settle(t, s, a)
	for i := 0; i < 1000; i++ {
		w, err := s.Work.Ensure(fmt.Sprint("stream/", i), fmt.Sprint("repo/", i%10), fmt.Sprint("Outcome ", i))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Work.Assign(w.ID, a.ID, "owner"); err != nil {
			t.Fatal(err)
		}
	}
	incorporated := map[string]bool{}
	for {
		x, err := s.Work.AgentSnapshot(a.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(x.Pending) == 0 {
			break
		}
		if len(x.Pending) > 24 {
			t.Fatal("unbounded management input")
		}
		for id := range x.Pending {
			if incorporated[id] {
				t.Fatal("acknowledged input was redelivered")
			}
			incorporated[id] = true
		}
		if err = s.Work.Checkpoint(a.ID, "batch", checkpoint(t, s, a)); err != nil {
			t.Fatal(err)
		}
	}
	if len(incorporated) != 1000 {
		t.Fatalf("only incorporated %d commitments", len(incorporated))
	}
}

func TestIntakeMigrationPreservesEvidenceFeedbackAndHistory(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	w, err := s.Work.Ensure("github/example/query", "https://github.com/example/query", "query reviews")
	if err != nil {
		t.Fatal(err)
	}
	w = commit(t, s, w, work.Event{Type: "watch", Source: work.Source{Kind: "github", URL: "doc"}})
	w = commit(t, s, w, observed("v1", 1))
	w = commit(t, s, w, prepared("v1", 0))
	w = commit(t, s, w, work.Event{Type: "feedback", Actor: "owner", Target: "doc", Text: "Preserve the nested example"})
	for range 2 {
		if err = s.Work.MigrateIntake(); err != nil {
			t.Fatal(err)
		}
	}
	intake, err := s.Work.Intake()
	if err != nil {
		t.Fatal(err)
	}
	if intake.Reviews["doc"].Feedback != "Preserve the nested example" || intake.Reviews["doc"].Version != "v1" || len(intake.Sources) != 1 {
		t.Fatal("migration discarded review context")
	}
	old, _ := s.Work.Get(w.ID)
	events, err := s.Work.History(old.ID, 0)
	if err != nil || len(events) != 4 {
		t.Fatal("source history lost")
	}
}

func TestOwnerDecisionRetainsAccountableFollowThrough(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	a := agent(t, s, "General")
	settle(t, s, a)
	w := create(t, s)
	var err error
	w, err = s.Work.Assign(w.ID, a.ID, "owner")
	if err != nil {
		t.Fatal(err)
	}
	w = commit(t, s, w, observed("v1", 1))
	w = commit(t, s, w, prepared("v1", 0))
	w = commit(t, s, w, work.Event{Type: "decide", Actor: "owner", Target: "doc", Text: "Proceed with the nested example"})
	if w.Reviews["doc"].NextOwner(w.Responsible()) != a.ID {
		t.Fatal("owner decision lost its follow-through owner")
	}
	if err = s.Work.Checkpoint(a.ID, "premature", checkpoint(t, s, a)); err == nil {
		t.Fatal("agent became idle with an unfulfilled owner decision")
	}
	w = commit(t, s, w, work.Event{Type: "follow-through", Actor: a.ID, Target: "doc", Context: w.Context.Revision, Review: work.Review{Version: "v1"}, Text: "Updated the nested example and verified it against the resolver fixture"})
	settle(t, s, a)
	if w.Reviews["doc"].NextOwner(w.Responsible()) != "none" {
		t.Fatal("verified follow-through remained unfulfilled")
	}
}

func TestManagementSubscriptionsDoNotCreateFeedbackCycles(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "ym.db"))
	defer s.Close()
	a, b := agent(t, s, "General"), agent(t, s, "Repository")
	if err := s.Work.Attach(a.ID, b.Workspace, "Coordinate repository management"); err != nil {
		t.Fatal(err)
	}
	if err := s.Work.Attach(b.ID, a.Workspace, "Reciprocal oversight"); err == nil {
		t.Fatal("reciprocal manager briefs can wake each other indefinitely")
	}
	if err := s.Work.SetRule(a.ID, "", false); err != nil {
		t.Fatal(err)
	}
	w, err := s.Work.Get(b.Workspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Work.AttachDefaults(w); err != nil {
		t.Fatal(err)
	}
	links, _ := s.Work.Attachments()
	if len(links) != 1 {
		t.Fatal("execution discovery bootstrapped more management subscriptions")
	}
}

func TestUnassignedIntakeDoesNotClaimSomeoneIsPreparingIt(t *testing.T) {
	for _, assigned := range []bool{false, true} {
		t.Run(fmt.Sprint(assigned), func(t *testing.T) {
			s := open(t, filepath.Join(t.TempDir(), "ym.db"))
			defer s.Close()
			w, err := s.Work.Ensure("github/example/query", "https://github.com/example/query", "query reviews")
			if err != nil {
				t.Fatal(err)
			}
			if assigned {
				w = commit(t, s, w, work.Event{Type: "manager", Actor: "discovery", Target: "native"})
			}
			w = commit(t, s, w, observed("v1", 1))
			if err = s.Work.RetireDiscoveredManagers(); err != nil {
				t.Fatal(err)
			}
			if err = s.Work.MigrateIntake(); err != nil {
				t.Fatal(err)
			}
			intake, err := s.Work.Intake()
			if err != nil {
				t.Fatal(err)
			}
			if intake.Reviews["doc"].State != "incoming" || intake.Reviews["doc"].NextOwner(intake.Responsible()) != "unassigned" {
				t.Fatal("unassigned source falsely claims active preparation")
			}
		})
	}
}
