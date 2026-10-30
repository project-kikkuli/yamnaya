package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func currentClaim(t *testing.T, w *verifyWorld) (Goal, Verification) {
	t.Helper()
	w.done(t, "research result")
	w.tick(t, time.Second)
	return w.state(t)
}

func TestExplicitWorkWithdrawsCompletionInEitherCommitOrder(t *testing.T) {
	for _, kind := range []string{"assignment", "go", "resume", "steer"} {
		for _, passFirst := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/pass-first=%v", kind, passFirst), func(t *testing.T) {
				w := newVerifyWorld(t)
				g, v := currentClaim(t, w)
				if passFirst {
					if _, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
						t.Fatal(err)
					}
				}
				switch kind {
				case "assignment":
					if _, err := w.core.AssignWork(w.manager.Session, "implementation", "implementation:authorized", "authorized implementation"); err != nil {
						t.Fatal(err)
					}
				case "go":
					it, err := w.core.Store.addItem(Item{Kind: KindGo, Session: w.manager.Session, Question: "implement"})
					if err != nil {
						t.Fatal(err)
					}
					if _, _, err = w.core.Go(w.board(t), it.Key()); err != nil {
						t.Fatal(err)
					}
				case "resume":
					if err := w.core.ResumeGoal(w.manager.Session, "chat-authorization", "authorized direct work"); err != nil {
						t.Fatal(err)
					}
				case "steer":
					if _, err := w.core.SteerContext(w.board(t), g.ID, "implement the research", Owner); err != nil {
						t.Fatal(err)
					}
				}
				if !passFirst {
					if _, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err == nil {
						t.Fatal("withdrawn research verifier passed")
					}
					if err := w.core.Store.commitStaleSupervisor(g); !errors.Is(err, ErrStale) {
						t.Fatalf("stale supervisor: %v", err)
					}
				}
				got, last := w.state(t)
				if got.State != GoalActive || got.WorkRevision != 1 || got.ActiveVerification != 0 || !got.DoneAt.IsZero() || !got.AckedAt.IsZero() {
					t.Fatalf("work did not reopen: %+v", got)
				}
				want := VerdictSuperseded
				if passFirst {
					want = VerdictPass
				}
				if last.Verdict != want {
					t.Fatalf("historical verdict %s, want %s", last.Verdict, want)
				}
				events, err := w.core.Store.GoalEvents(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				done := 0
				for _, e := range events {
					if e.Kind == EventDone {
						done++
					}
				}
				expected := 0
				if passFirst {
					expected = 1
				}
				if done != expected {
					t.Fatalf("%d done events, want %d", done, expected)
				}
			})
		}
	}
}

func (s *Store) commitStaleSupervisor(g Goal) error {
	g.State, g.Evidence = GoalStuck, "old supervisor observation"
	return s.SaveGoal(g)
}

func TestGoAuthorizationResolvesHistoricalOwnershipBeforeDelivery(t *testing.T) {
	for _, owner := range []string{"current manager", "old manager", "nested worker"} {
		t.Run(owner, func(t *testing.T) {
			w := newVerifyWorld(t)
			g, v := currentClaim(t, w)
			if _, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
				t.Fatal(err)
			}
			session := w.manager.Session
			switch owner {
			case "old manager":
				m := w.manager
				m.Session = "replacement"
				if err := w.core.Store.SaveManager(m); err != nil {
					t.Fatal(err)
				}
			case "nested worker":
				w.spawn("worker", session)
				w.spawn("nested", "worker")
				session = "nested"
			}
			it, err := w.core.Store.addItem(Item{Kind: KindGo, Session: session, Question: "build after research"})
			if err != nil {
				t.Fatal(err)
			}
			// Transition commits withdrawal before any caller can deliver the answer.
			if _, err = w.core.Store.Transition(it.Key(), ItemAnswered, GoAhead); err != nil {
				t.Fatal(err)
			}
			now, err := w.core.Store.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if now.State != GoalActive || now.WorkRevision != 1 {
				t.Fatalf("authorization delivered against done goal: %+v", now)
			}
			if _, err = w.core.Store.Transition(it.Key(), ItemAnswered, GoAhead); err == nil {
				t.Fatal("duplicate answer changed item")
			}
			// Reopening and answering the same authorization replays the stable source.
			if _, err = w.core.Store.Transition(it.Key(), ItemOpen, ""); err != nil {
				t.Fatal(err)
			}
			if _, err = w.core.Store.Transition(it.Key(), ItemAnswered, GoAhead); err != nil {
				t.Fatal(err)
			}
			now, _ = w.core.Store.Goal(g.ID)
			if now.WorkRevision != 1 {
				t.Fatalf("replay advanced revision: %d", now.WorkRevision)
			}
		})
	}
}

func TestAssignmentsRequireAcceptedGenerationSpecificTerminalReports(t *testing.T) {
	for _, status := range []string{"busy", "idle", "exited"} {
		t.Run(status, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.spawn("worker", w.manager.Session)
			a, err := w.core.AssignWork(w.manager.Session, "worker", "task-one", "implement CSV")
			if err != nil {
				t.Fatal(err)
			}
			replay, err := w.core.AssignWork(w.manager.Session, "worker", "task-one", "implement CSV")
			if err != nil || replay.ID != a.ID {
				t.Fatalf("replay: %+v %v", replay, err)
			}
			switch status {
			case "busy":
				w.set("worker", func(s *Session) { s.Status = Busy })
			case "idle":
				w.set("worker", func(s *Session) { s.Status = Idle; s.Result = "finished"; s.ResultAt = w.clock })
			case "exited":
				w.stop("worker")
			}
			if _, _, err = w.core.FinishGoal(w.manager.Session, "done", nil); err == nil {
				t.Fatal("process state settled assignment")
			}
			report, err := w.core.ReportAssignment(w.manager.Session, a.ID, "terminal result: commit abc")
			if err != nil {
				t.Fatal(err)
			}
			if err = w.core.AcceptAssignment("worker", a.ID, report); err == nil {
				t.Fatal("worker accepted own report")
			}
			if err = w.core.AcceptAssignment(w.manager.Session, a.ID, report+1); err == nil {
				t.Fatal("wrong report accepted")
			}
			if err = w.core.AcceptAssignment(w.manager.Session, a.ID, report); err != nil {
				t.Fatal(err)
			}
			if err = w.core.AcceptAssignment(w.manager.Session, a.ID, report); err != nil {
				t.Fatal(err)
			}
			next, err := w.core.AssignWork(w.manager.Session, "worker", "task-two", "add quoted fields")
			if err != nil {
				t.Fatal(err)
			}
			if next.Generation != a.Generation+1 {
				t.Fatalf("generation %d after %d", next.Generation, a.Generation)
			}
			if err = w.core.AcceptAssignment(w.manager.Session, next.ID, report); err == nil {
				t.Fatal("old report settled new generation")
			}
			if _, _, err = w.core.FinishGoal(w.manager.Session, "done", nil); err == nil {
				t.Fatal("new generation did not block")
			}
			report, err = w.core.ReportAssignment(w.manager.Session, next.ID, "quoted fields landed")
			if err != nil {
				t.Fatal(err)
			}
			if err = w.core.AcceptAssignment(w.manager.Session, next.ID, report); err != nil {
				t.Fatal(err)
			}
			_, v := currentClaim(t, w)
			if _, err = w.core.RecordVerdict(v.Session, w.goal.ID, VerdictPass, nil); err != nil {
				t.Fatal(err)
			}
			g, _ := w.state(t)
			if g.State != GoalDone {
				t.Fatalf("accepted terminal work did not pass: %s", g.State)
			}
		})
	}
}

func TestLaunchRegistersWorkBeforeProcessStartAndRetainsFailedAssignments(t *testing.T) {
	for _, fail := range []bool{false, true} {
		for _, nested := range []bool{false, true} {
			t.Run(fmt.Sprintf("failure=%v/nested=%v", fail, nested), func(t *testing.T) {
				w := newVerifyWorld(t)
				g, v := currentClaim(t, w)
				parent := w.manager.Session
				if nested {
					w.spawn("parent-worker", parent)
					parent = "parent-worker"
				}
				w.launches.during = func() {
					now, _ := w.core.Store.Goal(g.ID)
					assignments, _ := w.core.Store.Assignments(g.ID)
					if now.State != GoalActive || len(assignments) != 1 {
						t.Fatalf("launch before registration: %+v %+v", now, assignments)
					}
					if _, err := w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err == nil {
						t.Fatal("old verifier passed during launch")
					}
				}
				if fail {
					w.launches.fail = errors.New("exec failed")
				}
				_, err := w.core.StartNew(NewLaunch{Parent: parent, Prompt: "implement", Repo: "/repo/august", Session: "worker"})
				if (err != nil) != fail {
					t.Fatalf("launch: %v", err)
				}
				assignments, err := w.core.Store.Assignments(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				if len(assignments) != 1 || assignments[0].State != "outstanding" {
					t.Fatalf("launch lost obligation: %+v", assignments)
				}
			})
		}
	}
}

func TestRoutineCommunicationPreservesCompletionClaim(t *testing.T) {
	for _, kind := range []string{"update", "brief", "message", "idle report", "ack", "artifact"} {
		t.Run(kind, func(t *testing.T) {
			w := newVerifyWorld(t)
			g, v := currentClaim(t, w)
			var err error
			switch kind {
			case "update":
				_, err = w.core.UpdateGoal(w.manager.Session, "still reviewing")
			case "brief":
				_, err = w.core.WriteBrief(w.manager.Session, Brief{Assessment: "ready", Changed: "research", Uncertainty: "none", Next: "verify", WaitingOn: "verifier"})
			case "message":
				_, err = w.core.Store.AddMessage(w.manager.Session, "routine update", "")
			case "idle report":
				_, _, err = w.core.RecordGoalEvent(g.ID, EventFinished, "routine research report", "")
			case "ack":
				err = w.core.AckGoal(g.ID)
			case "artifact":
				err = w.core.SetArtifact(w.manager.Session, "https://example.com/report")
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
				t.Fatal(err)
			}
			now, _ := w.state(t)
			if now.State != GoalDone || now.WorkRevision != g.WorkRevision {
				t.Fatalf("routine communication invalidated claim: %+v", now)
			}
		})
	}
}

func TestStaleAttemptAndSupervisorCannotReplaceCurrentCompletion(t *testing.T) {
	for _, transition := range []string{"new attempt", "verifier replacement", "kill", "resume"} {
		t.Run(transition, func(t *testing.T) {
			w := newVerifyWorld(t)
			g, v := currentClaim(t, w)
			switch transition {
			case "new attempt":
				w.done(t, "new claim")
			case "verifier replacement":
				next := v
				next.Session = "new-verifier"
				if err := w.core.Store.SaveVerification(next); err != nil {
					t.Fatal(err)
				}
			case "kill":
				next := g
				next.State = GoalKilled
				if err := w.core.Store.SaveGoal(next); err != nil {
					t.Fatal(err)
				}
			case "resume":
				if err := w.core.ResumeGoal(w.manager.Session, "chat", "more work"); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := w.core.Store.commitVerdict(g, v, VerdictPass, nil, w.clock); !errors.Is(err, ErrStale) {
				t.Fatalf("stale verdict: %v", err)
			}
			if err := w.core.Store.SaveVerification(v); !errors.Is(err, ErrStale) {
				t.Fatalf("stale supervisor verification save: %v", err)
			}
			if transition != "verifier replacement" {
				if err := w.core.Store.commitStaleSupervisor(g); !errors.Is(err, ErrStale) {
					t.Fatalf("stale goal save: %v", err)
				}
			}
		})
	}
}

func TestPRChangesRequireACurrentClaimAndFacts(t *testing.T) {
	for _, change := range []string{"add owned", "head", "CI", "threads", "merge commit", "cached head", "touched", "dropped"} {
		t.Run(change, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.landings[pr1] = Landing{State: "MERGED", Head: "head1", MergeCommit: "merge1", Passed: 1}
			_, _, err := w.core.RecordGoalEvent(w.goal.ID, EventPROpened, "opened", w.goal.ID+":pr:"+pr1)
			if err != nil {
				t.Fatal(err)
			}
			if change == "dropped" {
				if _, _, err = w.core.DropGoalPR(w.manager.Session, w.goal.ID, pr1, "not ours"); err != nil {
					t.Fatal(err)
				}
			}
			g, v := currentClaim(t, w)
			switch change {
			case "add owned":
				_, _, err = w.core.RecordGoalEvent(g.ID, EventPROpened, "new PR", g.ID+":pr:https://github.com/acme/august/pull/8")
			case "head":
				l := w.landings[pr1]
				l.Head = "head2"
				w.landings[pr1] = l
			case "CI":
				l := w.landings[pr1]
				l.Failed = 1
				w.landings[pr1] = l
			case "threads":
				l := w.landings[pr1]
				l.Unresolved = 1
				w.landings[pr1] = l
			case "merge commit":
				l := w.landings[pr1]
				l.MergeCommit = "merge2"
				w.landings[pr1] = l
			case "cached head":
				err = w.core.Store.SavePRStatus(pr1, PRStatus{State: "MERGED", Merge: PRMergeFacts{OID: "head2"}, Fetched: w.clock})
			case "touched":
				_, _, err = w.core.RecordGoalEvent(g.ID, EventPRTouched, "reviewed", g.ID+":touched:https://github.com/acme/august/pull/8")
			case "dropped":
				w.landings[pr1] = Landing{State: "OPEN", Failed: 1, Unresolved: 1}
			}
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil)
			pass := change == "touched" || change == "dropped"
			if (err == nil) != pass {
				t.Fatalf("change %s: pass=%v error=%v", change, pass, err)
			}
			now, _ := w.state(t)
			if !pass && now.State != GoalActive {
				t.Fatalf("changed facts left an unusable verification: %+v", now)
			}
			if !pass {
				events, err := w.core.Store.GoalEvents(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				if events[len(events)-1].Kind != EventFailedVerify || !strings.Contains(events[len(events)-1].Payload, "ym goal done again") {
					t.Fatalf("manager lacks recovery instruction: %+v", events)
				}
			}
		})
	}
}

func TestWorkReplaySurvivesRestartAndEndedGoalsStayEnded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := s.AddGoal(Goal{Ask: "work"})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.ResumeGoal(g.ID, "chat:one", "authorized", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ResumeGoal(g.ID, "chat:one", "authorized", time.Now()); err != nil {
		t.Fatal(err)
	}
	g, err = s.Goal(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g.WorkRevision != 1 {
		t.Fatalf("replay after restart: %d", g.WorkRevision)
	}
	for _, state := range []string{GoalDropped, GoalKilled} {
		current, err := s.Goal(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.State = state
		if err = s.SaveGoal(current); err != nil {
			t.Fatal(err)
		}
		if err = s.ResumeGoal(g.ID, "work:"+state, "new work", time.Now()); err == nil {
			t.Fatal("ended goal reopened")
		}
		current, _ = s.Goal(g.ID)
		if current.State != state {
			t.Fatalf("ended state changed: %s", current.State)
		}
	}
}

func TestCompletionClaimKeepsManagerEvidenceImmutable(t *testing.T) {
	w := newVerifyWorld(t)
	_, v := currentClaim(t, w)
	v.Summary = "supervisor changed claim"
	v.Evidence = []string{"unclaimed evidence"}
	if err := w.core.Store.SaveVerification(v); err != nil {
		t.Fatal(err)
	}
	_, got := w.state(t)
	if strings.Contains(got.Summary, "supervisor") || len(got.Evidence) != 0 {
		t.Fatalf("claim changed: %+v", got)
	}
}

func TestCompletionTransitionsRollbackWithTheirEvents(t *testing.T) {
	for _, transition := range []string{"open", "pass", "resume"} {
		t.Run(transition, func(t *testing.T) {
			w := newVerifyWorld(t)
			var g Goal
			var v Verification
			kind := EventVerifying
			if transition != "open" {
				g, v = currentClaim(t, w)
				kind = EventDone
			}
			if transition == "resume" {
				kind = EventWorkResumed
			}
			_, err := w.core.Store.db.Exec(`CREATE TRIGGER reject_event BEFORE INSERT ON ym_goal_events WHEN NEW.kind='` + kind + `' BEGIN SELECT RAISE(ABORT,'injected event failure'); END`)
			if err != nil {
				t.Fatal(err)
			}
			switch transition {
			case "open":
				_, _, err = w.core.FinishGoal(w.manager.Session, "claim", nil)
			case "pass":
				_, err = w.core.Store.commitVerdict(g, v, VerdictPass, nil, w.clock)
			case "resume":
				err = w.core.ResumeGoal(w.manager.Session, "chat-failure", "work")
			}
			if err == nil {
				t.Fatal("transition committed without event")
			}
			now, err := w.core.Store.Goal(w.goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			all, err := w.core.Store.Verifications(w.goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if transition == "open" {
				if now.State != GoalActive || len(all) != 0 {
					t.Fatalf("partial open: %+v %+v", now, all)
				}
			} else {
				if now.State != GoalVerifying || now.WorkRevision != g.WorkRevision || all[0].Verdict != "" {
					t.Fatalf("partial verdict/work: %+v %+v", now, all)
				}
			}
		})
	}
}

func TestLaunchReplayAndNativeBindingRetainOneObligation(t *testing.T) {
	for _, native := range []bool{false, true} {
		t.Run(fmt.Sprintf("native=%v", native), func(t *testing.T) {
			w := newVerifyWorld(t)
			if native {
				a, err := w.core.AssignWork(w.manager.Session, "planned", "native:one", "implement")
				if err != nil {
					t.Fatal(err)
				}
				w.spawn("actual", "")
				if err = w.core.BindAssignment(w.manager.Session, a.ID, "actual"); err != nil {
					t.Fatal(err)
				}
				if err = w.core.BindAssignment(w.manager.Session, a.ID, "actual"); err != nil {
					t.Fatal(err)
				}
				nested, err := w.core.AssignWork("actual", "nested", "native:two", "nested implementation")
				if err != nil || nested.Goal != w.goal.ID {
					t.Fatalf("bound lineage: %+v %v", nested, err)
				}
			} else {
				launch := NewLaunch{Parent: w.manager.Session, Repo: "/repo/august", Prompt: "implement", Source: "launch:stable"}
				for range 2 {
					if _, err := w.core.StartNew(launch); err != nil {
						t.Fatal(err)
					}
				}
				starts := 0
				for _, n := range w.launches.starts {
					if n.Source == launch.Source {
						starts++
					}
				}
				if starts != 1 {
					t.Fatalf("replay launched %d workers", starts)
				}
			}
			var seq int
			var name, path string
			if err := w.core.Store.db.QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
				t.Fatal(err)
			}
			reopened, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			before, err := w.core.Store.Assignments(w.goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			after, err := reopened.Assignments(w.goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if len(before) != len(after) || after[0].State != "outstanding" || before[0].ID != after[0].ID {
				t.Fatalf("restart lost work: %+v %+v", before, after)
			}
		})
	}
}

func TestRoutineMetadataSurvivesStaleSupervisorAndVerdict(t *testing.T) {
	for _, kind := range []string{"artifact", "update", "ack"} {
		t.Run(kind, func(t *testing.T) {
			w := newVerifyWorld(t)
			g, v := currentClaim(t, w)
			var err error
			switch kind {
			case "artifact":
				err = w.core.SetArtifact(w.manager.Session, "https://example.com/current")
			case "update":
				w.clock = w.clock.Add(time.Minute)
				_, err = w.core.UpdateGoal(w.manager.Session, "current progress")
			case "ack":
				err = w.core.AckGoal(g.ID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = w.core.Store.commitStaleSupervisor(g); !errors.Is(err, ErrStale) {
				t.Fatalf("stale whole-goal overwrite: %v", err)
			}
			if _, err = w.core.Store.commitVerdict(g, v, VerdictPass, nil, w.clock); err != nil {
				t.Fatal(err)
			}
			now, _ := w.state(t)
			if kind == "artifact" && now.ArtifactURL != "https://example.com/current" {
				t.Fatalf("verdict erased artifact: %+v", now)
			}
			if kind == "update" && !now.LastMovedAt.Equal(w.clock) {
				t.Fatalf("verdict erased progress clock: %+v", now)
			}
		})
	}
}

func TestVerifierLaunchCannotPublishAfterConcurrentWithdrawal(t *testing.T) {
	for _, kind := range []string{"resume", "new claim", "steer", "kill"} {
		t.Run(kind, func(t *testing.T) {
			w := newVerifyWorld(t)
			v := w.done(t, "initial claim")
			g, _ := w.core.Store.Goal(w.goal.ID)
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			w.launches.during = func() {
				var err error
				switch kind {
				case "resume":
					err = w.core.ResumeGoal(w.manager.Session, "chat", "more work")
				case "new claim":
					_, _, err = w.core.FinishGoal(w.manager.Session, "revised claim", nil)
				case "steer":
					_, err = w.core.SteerContext(w.board(t), g.ID, "implement", Owner)
				case "kill":
					current, _ := w.core.Store.Goal(g.ID)
					current.State = GoalKilled
					err = w.core.Store.SaveGoal(current)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err = w.core.verifyOne(w.board(t), o, w.manager, g, v); !errors.Is(err, ErrStale) {
				t.Fatalf("stale verifier launch published: %v", err)
			}
			if len(w.launches.forgot) != 1 {
				t.Fatalf("withdrawn verifier left running: %+v", w.launches.forgot)
			}
			all, err := w.core.Store.Verifications(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if all[0].Verdict != VerdictSuperseded {
				t.Fatalf("launch restored old attempt: %+v", all)
			}
		})
	}
}

func TestBoardShowsCommittedCompletionState(t *testing.T) {
	for _, state := range []string{GoalVerifying, GoalDone, GoalActive} {
		t.Run(state, func(t *testing.T) {
			w := newVerifyWorld(t)
			_, v := currentClaim(t, w)
			if state != GoalVerifying {
				if _, err := w.core.RecordVerdict(v.Session, w.goal.ID, VerdictPass, nil); err != nil {
					t.Fatal(err)
				}
			}
			if state == GoalActive {
				if err := w.core.ResumeGoal(w.manager.Session, "direct-chat", "more work"); err != nil {
					t.Fatal(err)
				}
			}
			n, ok := w.board(t).Node(w.manager.ID)
			want := map[string]string{GoalVerifying: StandVerifying, GoalDone: StandDone, GoalActive: StandMoving}[state]
			if !ok || n.Goal.State != state || n.Standing != want {
				t.Fatalf("board: %+v, want %s/%s", n, state, want)
			}
		})
	}
}
