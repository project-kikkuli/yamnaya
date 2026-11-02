package core

import (
	"errors"
	"testing"
)

func TestRemoteAssignmentReopensCompletionWithoutGo(t *testing.T) {
	for _, entry := range []string{"native", "ym-new"} {
		for _, state := range []string{GoalVerifying, GoalDone, GoalKilled, GoalDropped} {
			t.Run(entry+"/"+state, func(t *testing.T) {
				f, repo := remoteWorkFleet(t)
				g, m := f.a.goal(t, "follow-on work", "/repo/app")
				if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "root", Session: "worker", Source: "root"}); err != nil {
					t.Fatal(err)
				}
				syncWork(t, f)
				all, _ := f.a.core.Store.Assignments(g.ID)
				root := all[0]
				if _, err := f.b.core.ReportAssignment("worker", root.ID, "root complete"); err != nil {
					t.Fatal(err)
				}
				syncWork(t, f)
				all, _ = f.a.core.Store.Assignments(g.ID)
				root = all[0]
				if err := f.a.core.AcceptAssignment(m.Session, root.ID, root.Report); err != nil {
					t.Fatal(err)
				}
				syncWork(t, f)
				if _, _, err := f.a.core.FinishGoal(m.Session, "root complete", nil); err != nil {
					t.Fatal(err)
				}
				f.a.supervise(t)
				v, err := f.a.core.Store.OpenVerification(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				if state == GoalDone {
					if _, err = f.a.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err != nil {
						t.Fatal(err)
					}
				} else if state != GoalVerifying {
					current, _ := f.a.core.Store.Goal(g.ID)
					current.State = state
					if err = f.a.core.Store.SaveGoal(current); err != nil {
						t.Fatal(err)
					}
				}
				before, _ := f.a.core.Store.Goal(g.ID)
				request := func() Assignment {
					t.Helper()
					if entry == "ym-new" {
						if _, err := f.b.core.StartNew(NewLaunch{Parent: "worker", Repo: "/repo/app", Prompt: "authorized follow-on", Session: "child", Source: "follow-on"}); err != nil {
							t.Fatal(err)
						}
						return Assignment{}
					}
					a, err := f.b.core.AssignWork("worker", "child", "follow-on", "authorized follow-on")
					if err != nil && !errors.Is(err, ErrWorkPending) {
						t.Fatal(err)
					}
					return a
				}
				request()
				if len(f.b.launches.starts) != 1 {
					t.Fatal("follow-on launched before origin reopened")
				}
				restartWorkHost(t, f.a)
				restartWorkHost(t, f.b)
				syncWork(t, f)
				current, _ := f.a.core.Store.Goal(g.ID)
				all, _ = f.a.core.Store.Assignments(g.ID)
				if state == GoalKilled || state == GoalDropped {
					if current.State != state || current.WorkRevision != before.WorkRevision || len(all) != 1 || len(f.b.launches.starts) != 1 {
						t.Fatalf("revoked goal reopened: %+v assignments=%+v", current, all)
					}
					return
				}
				if current.State != GoalActive || current.WorkRevision != before.WorkRevision+1 || current.ActiveVerification != 0 || !current.DoneAt.IsZero() || len(all) != 2 {
					t.Fatalf("follow-on did not reopen atomically: %+v assignments=%+v", current, all)
				}
				if state == GoalVerifying {
					if _, err = f.a.core.RecordVerdict(v.Session, g.ID, VerdictPass, nil); err == nil {
						t.Fatal("stale verifier passed after follow-on assignment")
					}
				}
				next := all[1]
				request()
				restartWorkHost(t, f.a)
				restartWorkHost(t, f.b)
				syncWork(t, f)
				current, _ = f.a.core.Store.Goal(g.ID)
				all, _ = f.a.core.Store.Assignments(g.ID)
				wantStarts := 1
				if entry == "ym-new" {
					wantStarts++
				} else if err = f.b.core.SettleAssignment(next.ID, "child", "worker"); err != nil {
					t.Fatal(err)
				}
				if len(all) != 2 || all[1].ID != next.ID || all[1].Generation != next.Generation || current.WorkRevision != before.WorkRevision+1 || len(f.b.launches.starts) != wantStarts {
					t.Fatalf("replay duplicated work: %+v %+v starts=%d", current, all, len(f.b.launches.starts))
				}
				syncWork(t, f)
				if _, _, err = f.a.core.FinishGoal(m.Session, "premature", nil); err == nil {
					t.Fatal("follow-on outstanding generation did not block completion")
				}
				if err = f.a.core.AcceptAssignment(m.Session, next.ID, root.Report); err == nil {
					t.Fatal("root report settled follow-on")
				}
				if _, err = f.b.core.ReportAssignment("child", next.ID, "follow-on complete"); err != nil {
					t.Fatal(err)
				}
				syncWork(t, f)
				all, _ = f.a.core.Store.Assignments(g.ID)
				if err = f.a.core.AcceptAssignment(m.Session, next.ID, all[1].Report); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestTerminalDispatchCannotBeReauthorizedByStaleCourier(t *testing.T) {
	for _, terminal := range []string{"final refusal", "terminal report"} {
		t.Run(terminal, func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			g, m := f.a.goal(t, "work", "/repo/app")
			if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
				t.Fatal(err)
			}
			pending, err := f.a.core.Store.pendingLaunches()
			if err != nil || len(pending) != 1 {
				t.Fatalf("pending authority: %+v %v", pending, err)
			}
			stale := pending[0]
			if terminal == "final refusal" {
				err = f.a.core.AuthorizeWork(stale.Assignment.ID, func(RemoteAssignment) error { return Final(errors.New("repository refused")) })
				if !errors.Is(err, ErrFinal) {
					t.Fatalf("final refusal: %v", err)
				}
			} else if _, err = f.a.core.ReportAssignment(m.Session, stale.Assignment.ID, "manager reports canceled launch"); err != nil {
				t.Fatal(err)
			}
			all, _ := f.a.core.Store.Assignments(g.ID)
			for _, phase := range []string{"before acceptance", "restart before acceptance", "after acceptance", "restart after acceptance"} {
				if phase == "after acceptance" {
					if err := f.a.core.AcceptAssignment(m.Session, stale.Assignment.ID, all[0].Report); err != nil {
						t.Fatal(err)
					}
				}
				if phase == "restart before acceptance" || phase == "restart after acceptance" {
					restartWorkHost(t, f.a)
				}
				err := f.a.core.AuthorizeWork(stale.Assignment.ID, func(w RemoteAssignment) error {
					t.Errorf("%s: stale courier resurrected terminal assignment %s", phase, w.Assignment.ID)
					return nil
				})
				if !errors.Is(err, ErrWorkRevoked) {
					t.Fatalf("%s: terminal dispatch authority: %v", phase, err)
				}
			}
			if _, err = f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "fresh work", Session: "worker", Source: "fresh"}); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			all, _ = f.a.core.Store.Assignments(g.ID)
			if len(all) != 2 || all[1].Generation <= stale.Assignment.Generation || len(f.b.launches.starts) != 1 || f.b.launches.starts[0].Assignment != all[1].ID {
				t.Fatalf("fresh generation lost: %+v starts=%+v", all, f.b.launches.starts)
			}
		})
	}
}
