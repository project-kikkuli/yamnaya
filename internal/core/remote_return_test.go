package core

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type noOriginRPCCourier struct{ *hostNet }

func (n noOriginRPCCourier) StartWork(from, host string, w RemoteAssignment) error {
	if host == w.Origin {
		return errors.New("origin must launch locally without self RPC")
	}
	return n.hostNet.StartWork(from, host, w)
}

type assignmentEndLaunches struct{ *fakeLaunches }

func (l assignmentEndLaunches) EndAssignment(id, goal string) (string, error) {
	for _, n := range l.starts {
		if n.Assignment == id {
			return l.End(n.Session, goal)
		}
	}
	return "", ErrNotFound
}

func TestNestedWorkReturnsToOriginWithoutSelfRPC(t *testing.T) {
	for _, harness := range []string{"claude", "codex"} {
		for _, driver := range []string{"origin", "courier"} {
			for _, kill := range []string{"never", "before launch", "during launch", "after launch"} {
				t.Run(harness+"/"+driver+"/"+kill, func(t *testing.T) {
					f, repo := remoteWorkFleet(t)
					f.net.hosts["a"] = f.a
					g, m := f.a.goal(t, "returning work", "/repo/app")
					if _, err := f.a.core.StartNew(NewLaunch{Parent: m.Session, Repo: repo, Prompt: "root", Session: "root", Source: "root"}); err != nil {
						t.Fatal(err)
					}
					syncWork(t, f)
					f.b.core.Remotes = &remoteDisconnected{fakeRemotes: fakeRemotes{boards: []RemoteBoard{{Host: "a", Goals: []Goal{{Repo: "/repo/app"}}}}}}
					n := NewLaunch{Parent: "root", Repo: "/repo/app", Prompt: "return home", Session: "child", Source: "return", Harness: harness}
					if _, err := f.b.core.StartNew(n); err != nil {
						t.Fatal(err)
					}
					// Stage the actual nested approval and launch request without a courier dispatch.
					for range 3 {
						for _, route := range []struct{ from, to *orgWorld }{{f.b, f.a}, {f.a, f.b}} {
							rows, err := route.from.core.Store.PendingRelays()
							if err != nil {
								t.Fatal(err)
							}
							for _, r := range rows {
								var message WorkMessage
								if r.Kind == RelayWork && r.Dest == route.to.core.SelfName() && json.Unmarshal([]byte(r.Body), &message) == nil {
									if err = route.to.core.WorkReceive(message); err != nil {
										t.Fatal(err)
									}
								}
							}
						}
					}
					pending, err := f.a.core.Store.pendingLaunches()
					if err != nil || len(pending) != 1 || pending[0].Host != "a" {
						t.Fatalf("return intent missing: %+v %v", pending, err)
					}
					child := pending[0].Assignment
					if kill == "before launch" {
						if _, err = f.a.core.KillGoal(f.a.board(t), g.ID); err != nil {
							t.Fatal(err)
						}
					}
					restartWorkHost(t, f.a)
					restartWorkHost(t, f.b)
					courier := noOriginRPCCourier{f.net}
					var killed chan error
					if kill == "during launch" {
						current, err := f.a.core.Store.Goal(g.ID)
						if err != nil {
							t.Fatal(err)
						}
						killed = make(chan error, 1)
						f.a.launches.during = func() {
							current.State = GoalKilled
							go func() { killed <- f.a.core.Store.SaveGoal(current) }()
							select {
							case err := <-killed:
								t.Fatalf("kill committed inside local authority: %v", err)
							default:
							}
						}
					}
					if driver == "origin" {
						err = f.a.core.dispatchOwn([]string{"b", "c"}, courier)
					} else {
						err = f.b.core.dispatchCarried("a", pending, []string{"a", "c"}, courier)
					}
					if err != nil {
						t.Fatal(err)
					}
					if killed != nil {
						if err := <-killed; err != nil {
							t.Fatal(err)
						}
						syncWork(t, f)
						worker := "child"
						if harness == "codex" {
							worker = "thread-2"
						}
						if _, live := f.a.board(t).Session(worker); live {
							t.Fatal("launch-first child escaped durable cancellation")
						}
						return
					}
					wantStarts := 2 // manager and returning child
					if kill == "before launch" {
						wantStarts--
					}
					if len(f.a.launches.starts) != wantStarts {
						t.Fatalf("return launch count: %d, want %d", len(f.a.launches.starts), wantStarts)
					}
					if kill == "before launch" {
						return
					}
					syncWork(t, f)
					all, _ := f.a.core.Store.Assignments(g.ID)
					child = all[1]
					worker := "child"
					if harness == "codex" {
						worker = "thread-2"
					}
					if child.Session != worker {
						t.Fatalf("origin did not bind local identity: %+v", child)
					}
					if _, err = f.b.core.StartNew(n); err != nil {
						t.Fatal(err)
					}
					restartWorkHost(t, f.a)
					restartWorkHost(t, f.b)
					syncWork(t, f)
					if len(f.a.launches.starts) != wantStarts {
						t.Fatal("return replay duplicated process")
					}
					if kill == "after launch" {
						if _, err = f.a.core.KillGoal(f.a.board(t), g.ID); err != nil {
							t.Fatal(err)
						}
						restartWorkHost(t, f.a)
						syncWork(t, f)
						if _, live := f.a.board(t).Session(worker); live {
							t.Fatal("local returning child survived origin kill")
						}
						return
					}
					report, err := f.a.core.ReportAssignment(worker, child.ID, "returned result")
					if err != nil {
						t.Fatal(err)
					}
					if err = f.a.core.AcceptAssignment(m.Session, child.ID, report); err != nil {
						t.Fatal(err)
					}
				})
			}
		}
	}
}

func TestOldGoalRevocationPreservesReownedWorker(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "connected", true: "restart"}[restart], func(t *testing.T) {
			f, repo := remoteWorkFleet(t)
			f.b.core.Launches = assignmentEndLaunches{f.b.launches}
			g1, m1 := f.a.goal(t, "first", "/repo/app")
			if _, err := f.a.core.StartNew(NewLaunch{Parent: m1.Session, Repo: repo, Prompt: "first", Session: "worker", Source: "first"}); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			all, _ := f.a.core.Store.Assignments(g1.ID)
			if _, err := f.b.core.ReportAssignment("worker", all[0].ID, "first complete"); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			all, _ = f.a.core.Store.Assignments(g1.ID)
			if err := f.a.core.AcceptAssignment(m1.Session, all[0].ID, all[0].Report); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			old, err := f.b.core.Store.remoteWork(all[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			g2, m2 := f.a.goal(t, "second", "/repo/app")
			next, err := f.a.core.AssignWork(m2.Session, "b/worker", "second", "new authorized work")
			if err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			if restart {
				restartWorkHost(t, f.a)
				restartWorkHost(t, f.b)
			}
			if _, err = f.a.core.KillGoal(f.a.board(t), g1.ID); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			if err = f.b.core.WorkReceive(WorkMessage{Op: "revoke", Work: old}); err != nil {
				t.Fatal(err)
			}
			if _, live := f.b.board(t).Session("worker"); !live || strings.Contains(strings.Join(f.b.launches.forgot, ","), "worker") {
				t.Fatal("historical goal cancellation killed reowned worker")
			}
			owner, err := f.b.core.remoteOwner("worker")
			if err != nil || owner.Assignment.ID != next.ID || owner.Goal != g2.ID {
				t.Fatalf("new ownership lost: %+v %v", owner, err)
			}
			if _, err = f.a.core.KillGoal(f.a.board(t), g2.ID); err != nil {
				t.Fatal(err)
			}
			syncWork(t, f)
			if _, live := f.b.board(t).Session("worker"); live {
				t.Fatal("current owner cancellation failed")
			}
		})
	}
}
