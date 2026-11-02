package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/spf13/cobra"
)

func launchAssignmentManager(t *testing.T, st *core.Store, session string) (core.Goal, core.Manager) {
	t.Helper()
	g, err := st.AddGoal(core.Goal{Ask: "implement work"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.AddManager(core.Manager{Role: core.RoleManager, GoalID: g.ID, Session: session})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	g.ManagerID = m.ID
	if err := st.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	return g, m
}

func TestAssignmentSettlementSurvivesLauncherRestartAndReplay(t *testing.T) {
	for _, method := range []string{"find", "await", "launch-link", "launched", "start-replay"} {
		for _, persisted := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/persisted=%v", method, persisted), func(t *testing.T) {
				db, _ := fakeCodex(t)
				state := t.TempDir()
				storePath := filepath.Join(state, "ym.db")
				st, err := core.OpenStore(storePath)
				if err != nil {
					t.Fatal(err)
				}
				g, m := launchAssignmentManager(t, st, "manager")
				k := &core.Core{Store: st, StateDir: state, Procs: func() ([]proc.Proc, error) { return nil, nil }}
				a, err := k.AssignWork(m.Session, "launch:planned", "launch:one", "implement work")
				if err != nil {
					t.Fatal(err)
				}
				l := testLauncher(t, state)
				l.core = k
				t.Setenv("YAMNAYA_YM_SOCKET", l.srv.Socket)
				rec := launch.Record{Key: "worker", Harness: "codex", Parent: m.Session, Assignment: a.ID, ConfigRoot: os.Getenv("CODEX_HOME"), Worktree: launch.Worktree{Path: t.TempDir()}, Started: time.Now()}
				if err = l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				if initial, err := l.settle(rec); err != nil || initial.Session != "" {
					t.Fatalf("identity invented before Codex recorded it: %+v %v", initial, err)
				}
				worker := "019a0000-0000-7000-8000-000000000099"
				if _, err = db.Exec(`INSERT INTO threads (id,rollout_path,created_at,updated_at,cwd) VALUES (?,'',?,?,?)`, worker, time.Now().Unix(), time.Now().Unix(), rec.Worktree.Path); err != nil {
					t.Fatal(err)
				}
				if persisted {
					rec.Session = worker
					if err = l.records.Save(rec); err != nil {
						t.Fatal(err)
					}
				}
				if err := st.Close(); err != nil {
					t.Fatal(err)
				}
				st, err = core.OpenStore(storePath)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				k = &core.Core{Store: st, StateDir: state, Procs: func() ([]proc.Proc, error) { return nil, nil }}
				l = launcher{srv: l.srv, conf: l.conf, records: l.records, core: k}
				settle := func() {
					t.Helper()
					switch method {
					case "find":
						if _, err := l.find(rec.Key); err != nil {
							t.Fatal(err)
						}
					case "await":
						stored, _ := l.records.Find(rec.Key)
						if _, err := l.await(stored); err != nil {
							t.Fatal(err)
						}
					case "launch-link":
						with := func(run runFunc) func(*cobra.Command, []string) error {
							return func(cmd *cobra.Command, args []string) error { return run(&env{core: k}, cmd, args) }
						}
						cmd := linkLaunchCmd(with)
						cmd.SetArgs([]string{rec.Key})
						if err := cmd.Execute(); err != nil {
							t.Fatal(err)
						}
					case "launched":
						rows := l.Launched()
						if len(rows) != 1 || rows[0].Session != worker {
							t.Fatalf("launched did not settle actual identity: %+v", rows)
						}
					case "start-replay":
						started, err := l.Start(core.NewLaunch{Assignment: a.ID, Harness: "invalid", Parent: "wrong-parent"})
						if err != nil || started.Session != worker || started.Key != rec.Key {
							t.Fatalf("assignment replay started another launch: %+v %v", started, err)
						}
					}
				}
				settle()
				all, err := st.Assignments(g.ID)
				if err != nil || len(all) != 1 || all[0].Session != worker || all[0].Generation != a.Generation || all[0].State != "outstanding" {
					t.Fatalf("settlement changed the obligation: %+v %v", all, err)
				}
				report, err := k.ReportAssignment(worker, a.ID, "implemented work")
				if err != nil {
					t.Fatal(err)
				}
				if err = k.AcceptAssignment(m.Session, a.ID, report); err != nil {
					t.Fatal(err)
				}
				_, next := launchAssignmentManager(t, st, "next-manager")
				if _, err = k.AssignWork(next.Session, worker, "launch:next", "new work"); err != nil {
					t.Fatal(err)
				}
				settle()
				lineage, err := st.Lineage()
				if err != nil || lineage.Parent[worker] != next.Session {
					t.Fatalf("old launch reclaimed reassigned worker: %+v %v", lineage, err)
				}
			})
		}
	}
}

func TestOriginAuthorityBindsRealLauncherWithoutNestedStoreWrite(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, await := range []bool{false, true} {
		t.Run(fmt.Sprintf("await=%v", await), func(t *testing.T) {
			db, ran := fakeCodex(t)
			state := t.TempDir()
			path := filepath.Join(state, "ym.db")
			st, err := core.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { st.Close() }()
			g, m := launchAssignmentManager(t, st, "manager")
			k := &core.Core{Store: st, StateDir: state, Procs: func() ([]proc.Proc, error) { return nil, nil }}
			a, err := k.AssignWork(m.Session, "planned", "return", "returning work")
			if err != nil {
				t.Fatal(err)
			}
			l := testLauncher(t, state)
			l.core = k
			k.Launches = l
			n := core.NewLaunch{Harness: "codex", Parent: "b/root", Directory: t.TempDir(), Prompt: "returning work", Slug: "return", Await: await}
			w := core.RemoteAssignment{Origin: k.SelfName(), Host: k.SelfName(), Goal: g.ID, Worker: "planned", Assignment: a, Parent: "b/root", Launch: &n}
			if err = k.WorkReceive(core.WorkMessage{Op: "launch-request", Work: w}); err != nil {
				t.Fatal(err)
			}
			worker := "019a0000-0000-7000-8000-000000000088"
			if await {
				recordThread(t, db, ran, worker)
			}
			if err = os.MkdirAll(l.records.Dir, 0700); err != nil {
				t.Fatal(err)
			}
			lock, err := os.OpenFile(filepath.Join(l.records.Dir, "assignment-start.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
				t.Fatal(err)
			}
			authorized := make(chan error, 1)
			go func() {
				authorized <- k.AuthorizeWork(a.ID, func(core.RemoteAssignment) error {
					t.Error("local origin called remote start")
					return nil
				})
			}()
			select {
			case err = <-authorized:
				syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			case <-time.After(10 * time.Second):
				syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
				<-authorized
				t.Fatal("origin writer waited for an ordinary launch's record lock")
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err = core.OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			k.Store = st
			if !await {
				cwd := resumed(t, ran)
				if _, err = db.Exec(`INSERT INTO threads (id,rollout_path,created_at,updated_at,cwd) VALUES (?,'',?,?,?)`, worker, time.Now().Unix(), time.Now().Unix(), cwd); err != nil {
					t.Fatal(err)
				}
				rows := l.Launched()
				if len(rows) != 1 || rows[0].Session != worker {
					t.Fatalf("async return did not settle after restart: %+v", rows)
				}
			}
			all, err := st.Assignments(g.ID)
			if err != nil || len(all) != 1 || all[0].Session != worker || all[0].Generation != a.Generation {
				t.Fatalf("origin binding lost: %+v %v", all, err)
			}
			if err = k.AuthorizeWork(a.ID, func(core.RemoteAssignment) error { t.Error("replay started worker"); return nil }); err != nil {
				t.Fatal(err)
			}
			report, err := k.ReportAssignment(worker, a.ID, "return complete")
			if err != nil {
				t.Fatal(err)
			}
			if err = k.AcceptAssignment(m.Session, a.ID, report); err != nil {
				t.Fatal(err)
			}
			if records, err := l.records.All(); err != nil || len(records) != 1 {
				t.Fatalf("replay created another launch record: %+v %v", records, err)
			}
		})
	}
}

func TestNonAwaitAssignmentLaunchPersistsIdentityBindingAndReusesProcess(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	db, ran := fakeCodex(t)
	state := t.TempDir()
	st, err := core.OpenStore(filepath.Join(state, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	_, m := launchAssignmentManager(t, st, "manager")
	k := &core.Core{Store: st, StateDir: state, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	a, err := k.AssignWork(m.Session, "launch:planned", "launch:start", "implement work")
	if err != nil {
		t.Fatal(err)
	}
	l := testLauncher(t, state)
	l.core = k
	n := core.NewLaunch{Assignment: a.ID, Harness: "codex", Parent: m.Session, Directory: t.TempDir(), Prompt: "implement work", Slug: "worker"}
	type result struct {
		started core.Started
		err     error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			started, err := l.Start(n)
			results <- result{started: started, err: err}
		}()
	}
	initial := <-results
	first := initial.started
	if initial.err != nil || first.Session != "" {
		t.Fatalf("non-await launch did not return pending identity: %+v %v", first, initial.err)
	}
	concurrent := <-results
	if concurrent.err != nil || concurrent.started.Key != first.Key || concurrent.started.Session != "" {
		t.Fatalf("concurrent replay created another launch: %+v %v", concurrent.started, concurrent.err)
	}
	cwd := resumed(t, ran)
	rec, ok := l.records.Find(first.Key)
	if !ok || rec.Assignment != a.ID {
		t.Fatalf("assignment not durable before discovery: %+v", rec)
	}
	rec.Starting, rec.NativePID = true, 0
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	second, err := l.Start(n)
	if err != nil || second.Key != first.Key || second.Session != "" {
		t.Fatalf("pending launch replay: %+v %v", second, err)
	}
	if confirmed, _ := l.records.Find(first.Key); confirmed.Starting || confirmed.NativePID == 0 {
		t.Fatalf("replay did not recover the confirmed pane: %+v", confirmed)
	}
	all, err := l.records.All()
	if err != nil || len(all) != 1 {
		t.Fatalf("replay created more than one launch: %+v %v", all, err)
	}
	worker := "019a0000-0000-7000-8000-000000000088"
	if _, err = db.Exec(`INSERT INTO threads(id,rollout_path,created_at,updated_at,cwd) VALUES (?,'',?,?,?)`, worker, time.Now().Unix(), time.Now().Unix(), cwd); err != nil {
		t.Fatal(err)
	}
	settled, err := l.Start(n)
	if err != nil || settled.Key != first.Key || settled.Session != worker {
		t.Fatalf("replayed launch did not settle: %+v %v", settled, err)
	}
	report, err := k.ReportAssignment(worker, a.ID, "implementation committed")
	if err != nil {
		t.Fatal(err)
	}
	if err = k.AcceptAssignment(m.Session, a.ID, report); err != nil {
		t.Fatal(err)
	}
}

func TestAssignmentSettlementFailuresStayVisibleAndPreserveRecords(t *testing.T) {
	for _, method := range []string{"find", "await", "launched", "start-replay"} {
		t.Run(method, func(t *testing.T) {
			state := t.TempDir()
			st, err := core.OpenStore(filepath.Join(state, "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			var logs []string
			k := &core.Core{Store: st, Log: func(s string) { logs = append(logs, s) }}
			l := testLauncher(t, state)
			l.core = k
			rec := launch.Record{Key: "worker", Harness: "codex", Session: "worker", Assignment: "missing", Parent: "manager", Running: true, Worktree: launch.Worktree{Path: t.TempDir()}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			switch method {
			case "find":
				_, err = l.find(rec.Key)
			case "await":
				_, err = l.await(rec)
			case "start-replay":
				_, err = l.Start(core.NewLaunch{Assignment: rec.Assignment})
			case "launched":
				rows := l.Launched()
				if len(rows) != 1 || rows[0].State != core.LaunchUnknown || len(logs) != 1 || !strings.Contains(logs[0], "missing") {
					t.Fatalf("failure hidden from board/log: %+v %v", rows, logs)
				}
			}
			if method != "launched" && err == nil {
				t.Fatal("settlement failure was discarded")
			}
			stored, ok := l.records.Find(rec.Key)
			if !ok || stored != rec {
				t.Fatalf("failed binding changed lifecycle metadata: %+v", stored)
			}
		})
	}
}

func TestAssignmentLaunchReplayDoesNotInventSuccessfulStart(t *testing.T) {
	l := testLauncher(t, t.TempDir())
	rec := launch.Record{Key: "worker", Harness: "codex", Assignment: "assignment", Starting: true, Started: time.Now().UTC(), Worktree: launch.Worktree{Path: t.TempDir()}}
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Start(core.NewLaunch{Assignment: rec.Assignment}); err == nil {
		t.Fatal("replay accepted a launch with no confirmed pane")
	}
	stored, ok := l.records.Find(rec.Key)
	if !ok || stored != rec {
		t.Fatalf("unconfirmed replay changed record: %+v", stored)
	}
}

func TestAssignmentSettlementReturnsDiscoveredIdentitySaveFailures(t *testing.T) {
	for _, method := range []string{"settle", "await"} {
		t.Run(method, func(t *testing.T) {
			db, _ := fakeCodex(t)
			blocked := filepath.Join(t.TempDir(), "records")
			if err := os.WriteFile(blocked, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			rec := launch.Record{Key: "worker", Harness: "codex", Assignment: "assignment", Parent: "manager", ConfigRoot: os.Getenv("CODEX_HOME"), Worktree: launch.Worktree{Path: t.TempDir()}, Started: time.Now()}
			if _, err := db.Exec(`INSERT INTO threads(id,rollout_path,created_at,updated_at,cwd) VALUES ('worker','',?,?,?)`, time.Now().Unix(), time.Now().Unix(), rec.Worktree.Path); err != nil {
				t.Fatal(err)
			}
			l := launcher{records: launch.Records{Dir: blocked}}
			var settled launch.Record
			var err error
			if method == "settle" {
				settled, err = l.settle(rec)
			} else {
				settled, err = l.await(rec)
			}
			if err == nil || !strings.Contains(err.Error(), blocked) || settled.Session != "worker" {
				t.Fatalf("discovered identity save failure was lost: %+v %v", settled, err)
			}
		})
	}
}
