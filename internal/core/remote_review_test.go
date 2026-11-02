package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func restartWorkHost(t *testing.T, w *orgWorld) {
	t.Helper()
	path := w.core.Store.memo.files[0]
	if err := w.core.Store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	w.core.Store = store
	t.Cleanup(func() { store.Close() })
}

func TestOriginReassignsRemoteWorkerWithoutRelaunch(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "connected", true: "disconnected restart"}[restart], func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			g, m := f.a.goal(t, "remote work", "/repo/app")
			if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "first", Session: "worker", Source: "first"}); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			all, _ := f.a.core.Store.Assignments(g.ID)
			old := all[0]
			if _, err := f.b.core.ReportAssignment("worker", old.ID, "first complete"); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			all, _ = f.a.core.Store.Assignments(g.ID)
			old = all[0]
			if err := f.a.core.AcceptAssignment(m.Session, old.ID, old.Report); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			f.net.down["b"] = restart
			next, err := f.a.core.AssignWork(m.Session, "b/worker", "second", "second work")
			if err != nil {
				t.Fatal(err)
			}
			replay, err := f.a.core.AssignWork(m.Session, "b/worker", "second", "second work")
			if err != nil || replay.ID != next.ID || replay.Generation != next.Generation {
				t.Fatalf("replay changed assignment: %+v %v", replay, err)
			}
			if restart {
				restartWorkHost(t, f.a)
				restartWorkHost(t, f.b)
			}
			f.net.down["b"] = false
			syncWork(t, f)
			if _, err = f.b.core.ReportAssignment("worker", old.ID, "old replay"); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			if err = f.a.core.AcceptAssignment(m.Session, next.ID, old.Report); err == nil {
				t.Fatal("old report accepted for new generation")
			}
			if _, err = f.b.core.ReportAssignment("worker", next.ID, "second complete"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.b.core.ReportAssignment("worker", next.ID, "second replay"); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			all, _ = f.a.core.Store.Assignments(g.ID)
			if len(all) != 2 || all[1].Generation <= old.Generation || all[1].Report == 0 || all[1].Report == old.Report {
				t.Fatalf("generation/report mismatch: %+v", all)
			}
			if err = f.a.core.AcceptAssignment(m.Session, next.ID, all[1].Report); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			route, err := f.b.core.Store.remoteWork(next.ID)
			if err != nil || route.State != "accepted" || route.Worker != "worker" || len(f.b.launches.starts) != 1 {
				t.Fatalf("worker replaced or route lost: %+v starts=%v %v", route, f.b.launches.starts, err)
			}
			if dest, _ := f.b.core.Store.Assignments(g.ID); len(dest) != 0 {
				t.Fatalf("destination took ledger authority: %+v", dest)
			}
		})
	}
}

func TestQueuedLaunchCannotOutliveOriginKill(t *testing.T) {
	for _, entry := range []string{"origin intent", "legacy RelayYM", "nested approved"} {
		for _, restart := range []bool{false, true} {
			t.Run(entry+map[bool]string{false: "", true: "/restart"}[restart], func(t *testing.T) {
				f, repo := remoteWorkFleet(t)
				g, m := f.a.goal(t, "remote work", "/repo/app")
				f.a.core.Remotes = &remoteDisconnected{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "b", Goals: []Goal{{Repo: repo}}}}}}
				if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
					t.Fatal(err)
				}
				pending, err := f.a.core.Store.pendingLaunches()
				if err != nil || len(pending) != 1 {
					t.Fatalf("missing durable launch: %+v %v", pending, err)
				}
				w := pending[0]
				if entry == "legacy RelayYM" {
					if err = f.a.core.Relay("b", RelayYM, relayYM{Args: []string{"work-receive", jsonOf(WorkMessage{Op: "launch", Work: w})}}); err != nil {
						t.Fatal(err)
					}
				}
				expected := 0
				if entry == "nested approved" {
					syncWork(t, f)
					expected = 1
					if _, err = f.b.core.StartNew(NewLaunch{Parent: "worker", Repo: "/repo/child", Prompt: "nested", Session: "child", Source: "nested"}); err != nil {
						t.Fatal(err)
					}
					rows, err := f.b.core.Store.PendingRelays()
					if err != nil {
						t.Fatal(err)
					}
					for _, r := range rows {
						var message WorkMessage
						if r.Kind == RelayWork && json.Unmarshal([]byte(r.Body), &message) == nil && message.Op == "assign" {
							if err = f.a.core.WorkReceive(message); err != nil {
								t.Fatal(err)
							}
						}
					}
				}
				if _, err = f.a.core.KillGoal(f.a.board(t), g.ID); err != nil {
					t.Fatal(err)
				}
				if restart {
					restartWorkHost(t, f.a)
					restartWorkHost(t, f.b)
				}
				syncWork(t, f)
				if err = f.b.core.WorkReceive(WorkMessage{Op: "launch", Work: w}); err != nil {
					t.Fatal(err)
				}
				syncWork(t, f)
				goal, _ := f.a.core.Store.Goal(g.ID)
				if goal.State != GoalKilled || len(f.b.launches.starts) != expected {
					t.Fatalf("queued work started after kill: goal=%+v launches=%+v", goal, f.b.launches.starts)
				}
			})
		}
	}
}

type interruptedWorkCourier struct {
	*hostNet
	afterStart bool
}

func (n interruptedWorkCourier) StartWork(from, host string, w RemoteAssignment) error {
	if n.afterStart {
		if err := n.hostNet.StartWork(from, host, w); err != nil {
			return err
		}
	}
	return ErrUnreachable
}

func TestLaunchDispatchFailureRetainsRevocationAndCancellation(t *testing.T) {
	for _, afterStart := range []bool{false, true} {
		t.Run(map[bool]string{false: "disconnect before start", true: "disconnect after start"}[afterStart], func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			g, m := f.a.goal(t, "work", "/repo/app")
			f.a.core.Remotes = &remoteDisconnected{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "b", Goals: []Goal{{Repo: repo}}}}}}
			if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
				t.Fatal(err)
			}
			if err := f.a.core.SyncRelays([]string{"b", "c"}, interruptedWorkCourier{hostNet: f.net, afterStart: afterStart}); err != nil {
				t.Fatal(err)
			}
			if len(f.b.launches.starts) != map[bool]int{false: 0, true: 1}[afterStart] {
				t.Fatalf("unexpected start: %+v", f.b.launches.starts)
			}
			f.net.down["b"] = true
			if _, err := f.a.core.KillGoal(f.a.board(t), g.ID); err != nil {
				t.Fatal(err)
			}
			restartWorkHost(t, f.a)
			restartWorkHost(t, f.b)
			f.net.down["b"] = false
			syncWork(t, f)
			if afterStart && !strings.Contains(strings.Join(f.b.launches.forgot, ","), "worker") {
				t.Fatalf("started process not canceled: %v", f.b.launches.forgot)
			}
			if _, live := f.b.board(t).Session("worker"); live {
				t.Fatal("worker survives revocation")
			}
		})
	}
}

func TestRelayOnlyOriginRequiresLiveLaunchAuthority(t *testing.T) {
	f := newFleet(t)
	const repo = "/repo/destination"
	g, m := f.b.goal(t, "work", "/repo/origin")
	f.b.core.Remotes = &remoteDisconnected{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "c", Goals: []Goal{{Repo: repo}}}}}}
	if _, err := f.b.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
		t.Fatal(err)
	}
	f.net.down["b"] = true
	syncWork(t, f)
	if len(f.c.launches.starts) != 0 {
		t.Fatal("disconnected authority started work")
	}
	f.net.down["b"] = false
	syncWork(t, f)
	if len(f.c.launches.starts) != 1 {
		t.Fatalf("live courier did not dispatch: %+v", f.c.launches.starts)
	}
	if _, err := f.b.core.KillGoal(f.b.board(t), g.ID); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	if _, live := f.c.board(t).Session("worker"); live {
		t.Fatal("remote-origin cancellation lost")
	}
}

type reviewPRSource struct {
	status PRStatus
	err    error
}

func (s reviewPRSource) PRStatus(string) (PRStatus, error) { return s.status, s.err }

func TestUncachedRemotePRSurvivesReportExitAndRestart(t *testing.T) {
	for _, mode := range []string{"owned", "touched", "dropped"} {
		t.Run(mode, func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			g, m := f.a.goal(t, "remote PR", "/repo/app")
			if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			f.b.set("worker", func(s *Session) { s.PRs = []PR{{URL: pr1}} })
			f.b.core.PRs = reviewPRSource{err: errors.New("status unavailable")}
			if _, err := f.b.core.RefreshPRs(f.b.board(t)); err == nil {
				t.Fatal("fixture did not exercise failed status hydration")
			}
			if mode == "dropped" {
				if _, _, err := f.a.core.DropGoalPR(m.Session, g.ID, pr1, "unrelated"); err != nil {
					t.Fatal(err)
				}
			}
			all, _ := f.a.core.Store.Assignments(g.ID)
			if _, err := f.b.core.ReportAssignment("worker", all[0].ID, "work complete"); err != nil {
				t.Fatal(err)
			}
			f.b.stop("worker")
			restartWorkHost(t, f.a)
			restartWorkHost(t, f.b)
			syncWork(t, f)
			all, _ = f.a.core.Store.Assignments(g.ID)
			if err := f.a.core.AcceptAssignment(m.Session, all[0].ID, all[0].Report); err != nil {
				t.Fatal(err)
			}
			f.a.core.PRs = reviewPRSource{err: errors.New("status unavailable")}
			if mode != "dropped" {
				if _, err := f.a.core.RefreshPRs(f.a.board(t)); err == nil {
					t.Fatal("origin did not retain failed hydration")
				}
			}
			f.a.core.Landings = landingCall(func(string) (Landing, error) { return Landing{}, errors.New("status unavailable") })
			_, v, err := f.a.core.FinishGoal(m.Session, "complete", nil)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(v.PRSnapshot, pr1) != (mode != "dropped") {
				t.Fatalf("uncached candidate lost: %+v", v)
			}
			f.a.supervise(t)
			status := PRStatus{State: "OPEN", Fetched: f.a.clock, Merge: PRMergeFacts{Author: "me", Viewer: "me"}}
			landing := Landing{State: "OPEN", Author: "me", Viewer: "me"}
			if mode == "touched" {
				status.Merge.Author = "other"
				landing.Author = "other"
			}
			f.a.core.PRs = reviewPRSource{status: status}
			if _, err = f.a.core.RefreshPRs(f.a.board(t)); err != nil {
				t.Fatal(err)
			}
			f.a.core.Landings = fakeLandings{pr1: landing}
			f.a.clock = f.a.clock.Add(time.Minute)
			f.a.supervise(t)
			goal, _ := f.a.core.Store.Goal(g.ID)
			if mode == "owned" && goal.State != GoalActive {
				t.Fatalf("open owned PR failed to block completion: %+v", goal)
			}
			if mode != "owned" {
				current, err := f.a.core.Store.OpenVerification(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.a.core.RecordVerdict(current.Session, g.ID, VerdictPass, nil); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestLaunchPermissionSerializesConcurrentKillAndRetainsCancellation(t *testing.T) {
	f, repo := remoteWorkFleet(t)
	g, m := f.a.goal(t, "work", "/repo/app")
	f.a.core.Remotes = &remoteDisconnected{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "b", Goals: []Goal{{Repo: repo}}}}}}
	if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
		t.Fatal(err)
	}
	all, _ := f.a.core.Store.Assignments(g.ID)
	g, err := f.a.core.Store.Goal(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	killed := make(chan error, 1)
	err = f.a.core.AuthorizeWork(all[0].ID, func(w RemoteAssignment) error {
		g.State = GoalKilled
		go func() { killed <- f.a.core.Store.SaveGoal(g) }()
		return f.b.core.StartWork(w)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = <-killed; err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	if len(f.b.launches.starts) != 1 {
		t.Fatal("launch order lost")
	}
	if _, live := f.b.board(t).Session("worker"); live {
		t.Fatal("concurrent kill lost cancellation")
	}
}

type failedWorkCancellation struct {
	*fakeLaunches
	fail bool
}

func (l *failedWorkCancellation) EndAssignment(id, goal string) (string, error) {
	if l.fail {
		return "", errors.New("process cancellation denied")
	}
	return l.End("worker", goal)
}

func TestRevocationSurvivesCancellationError(t *testing.T) {
	f, repo := remoteWorkFleet(t)
	g, m := f.a.goal(t, "work", "/repo/app")
	if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	cancel := &failedWorkCancellation{fakeLaunches: f.b.launches, fail: true}
	f.b.core.Launches = cancel
	if _, err := f.a.core.KillGoal(f.a.board(t), g.ID); err != nil {
		t.Fatal(err)
	}
	// The receiver refuses the revocation until cancellation succeeds.
	if err := f.a.core.SyncRelays([]string{"b", "c"}, f.net); err != nil {
		t.Fatal(err)
	}
	pending, err := f.a.core.Store.PendingRelays()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range pending {
		var w WorkMessage
		if r.Kind == RelayWork && json.Unmarshal([]byte(r.Body), &w) == nil && w.Op == "revoke" && strings.Contains(r.Error, "process cancellation denied") {
			found = true
		}
	}
	if !found {
		t.Fatal("failed cancellation lost durable delivery")
	}
	restartWorkHost(t, f.a)
	restartWorkHost(t, f.b)
	cancel.fail = false
	syncWork(t, f)
	if _, live := f.b.board(t).Session("worker"); live {
		t.Fatal("worker survived retried durable cancellation")
	}
}

type refusedWorkCourier struct{ *hostNet }

func (n refusedWorkCourier) StartWork(from, host string, w RemoteAssignment) error {
	return Final(errors.New("no repository matches requested work"))
}

func TestPermanentLaunchRefusalProducesAnAcceptableTerminalReport(t *testing.T) {
	f, repo := remoteWorkFleet(t)
	g, m := f.a.goal(t, "work", "/repo/app")
	if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "work", Session: "worker", Source: "root"}); err != nil {
		t.Fatal(err)
	}
	err := f.a.core.SyncRelays([]string{"b", "c"}, refusedWorkCourier{f.net})
	if !errors.Is(err, ErrFinal) {
		t.Fatalf("refusal identity lost: %v", err)
	}
	all, err := f.a.core.Store.Assignments(g.ID)
	if err != nil || len(all) != 1 || all[0].Report == 0 {
		t.Fatalf("permanent refusal has no terminal report: %+v %v", all, err)
	}
	if len(f.b.launches.starts) != 0 {
		t.Fatal("refused work started")
	}
	restartWorkHost(t, f.a)
	syncWork(t, f)
	if len(f.b.launches.starts) != 0 {
		t.Fatal("permanent refusal retried as a launch")
	}
	if err = f.a.core.AcceptAssignment(m.Session, all[0].ID, all[0].Report); err != nil {
		t.Fatal(err)
	}
	syncWork(t, f)
	route, err := f.b.core.Store.remoteWork(all[0].ID)
	if err != nil || route.State != "accepted" {
		t.Fatalf("refusal acceptance lost route: %+v %v", route, err)
	}
}
