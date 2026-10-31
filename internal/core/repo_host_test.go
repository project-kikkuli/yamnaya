package core

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// hostWorld reaches the host box, which alone holds the repository boxRepo names: a path under the test's own directory that no
// one creates.
func hostWorld(t *testing.T) (*orgWorld, *fakeRemotes, string) {
	w := newOrgWorld(t)
	boxRepo := filepath.Join(t.TempDir(), "box", "august")
	r := &fakeRemotes{out: "goal9: manager mgr9 runs it as 1234abcd", boards: []RemoteBoard{{Host: "box", At: w.clock,
		Goals: []Goal{{ID: "goal7", Repo: boxRepo, ManagerID: "mgr7", State: GoalActive}}}}}
	w.core.Remotes = r
	return w, r, boxRepo
}

// stray records a goal here whose repository is repo, as an older ym did for a row of another host.
func (w *orgWorld) stray(t *testing.T, ask, repo string) (Goal, Manager) {
	t.Helper()
	g, err := w.core.Store.AddGoal(Goal{Ask: ask, Repo: repo, CreatedAt: w.clock})
	if err != nil {
		t.Fatal(err)
	}
	m, err := w.core.Store.AddManager(Manager{Role: RoleManager, Name: shortName(ask), GoalID: g.ID, Repo: repo, Harness: "claude", Failures: 2})
	if err != nil {
		t.Fatal(err)
	}
	g.ManagerID = m.ID
	if err := w.core.Store.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	return g, m
}

func TestManagersStartOnTheHostHoldingTheirRepository(t *testing.T) {
	actions := map[string]func(*testing.T, *orgWorld, Manager){
		"relaunch": func(t *testing.T, w *orgWorld, m Manager) {
			if _, err := w.core.ReplaceManager(w.board(t), m.ID); err != nil {
				t.Fatal(err)
			}
		},
		"revive": func(t *testing.T, w *orgWorld, m Manager) {
			o, _ := w.core.LoadOrg()
			g := o.Goals[m.GoalID]
			g.State = GoalKilled
			w.core.Store.SaveGoal(g)
			if _, err := w.core.ReopenGoal(m.ID); err != nil {
				t.Fatal(err)
			}
			w.supervise(t)
		},
		"refresh": func(t *testing.T, w *orgWorld, m Manager) {
			o, _ := w.core.LoadOrg()
			if _, err := w.core.refresh(w.board(t), o, m, Live{}, 900_000, true); err != nil {
				t.Fatal(err)
			}
		},
		"supervise": func(t *testing.T, w *orgWorld, m Manager) {
			w.clock = w.clock.Add(time.Hour)
			w.supervise(t)
		},
	}
	for name, act := range actions {
		for _, remote := range []bool{false, true} {
			where := map[bool]string{false: "local", true: "remote"}[remote]
			t.Run(name+"/"+where, func(t *testing.T) {
				w, r, boxRepo := hostWorld(t)
				repo := t.TempDir()
				if remote {
					repo = boxRepo
				}
				g, m := w.stray(t, "explore the github org", repo)
				act(t, w, m)
				o, _ := w.core.LoadOrg()
				g, m = o.Goals[g.ID], o.Managers[m.ID]
				if !remote {
					if len(r.ran) != 0 || len(w.launches.starts) != 1 || w.launches.starts[0].Repo != repo || m.Retired || g.Ended() {
						t.Fatalf("ran %v, starts %+v, manager %+v, goal %s", r.ran, w.launches.starts, m, g.State)
					}
					return
				}
				want := [][]string{{"box", "goal", "--repo", boxRepo, "--", "explore the github org"}}
				if !reflect.DeepEqual(r.ran, want) || len(w.launches.starts) != 0 {
					t.Fatalf("ran %v, starts %+v; want only %v", r.ran, w.launches.starts, want)
				}
				if !m.Retired || g.State != GoalDropped {
					t.Fatalf("stray manager %+v, goal %s; want it retired and dropped", m, g.State)
				}
				if _, shown := w.board(t).Node(g.ID); shown {
					t.Errorf("the moved goal still shows here as %s", g.ID)
				}
			})
		}
	}
}

func TestNewWorkInAnotherHostsRepositoryStartsThere(t *testing.T) {
	starts := map[string]struct {
		start func(*Core, string) error
		want  []string
	}{
		"goal": {func(c *Core, repo string) error {
			_, _, err := c.NewGoal("explore the github org", repo, "codex")
			return err
		},
			[]string{"goal", "--repo", "", "--codex", "--", "explore the github org"}},
		"chief": {func(c *Core, repo string) error { _, err := c.NewChief("org", "watch it", repo, "", nil); return err },
			[]string{"chief", "--repo", "", "--remit", "watch it", "--", "org"}},
		"session": {func(c *Core, repo string) error {
			_, err := c.StartNew(NewLaunch{Prompt: "look around", Harness: "claude", Repo: repo})
			return err
		}, []string{"new", "--repo", "", "--", "look around"}},
	}
	for name, s := range starts {
		for _, remote := range []bool{false, true} {
			t.Run(name+"/"+map[bool]string{false: "local", true: "remote"}[remote], func(t *testing.T) {
				w, r, boxRepo := hostWorld(t)
				repo := t.TempDir()
				if remote {
					repo = boxRepo
				}
				if err := s.start(w.core, repo); err != nil {
					t.Fatal(err)
				}
				o, _ := w.core.LoadOrg()
				if !remote {
					if len(r.ran) != 0 || len(w.launches.starts) != 1 {
						t.Fatalf("ran %v, starts %d; want one start here", r.ran, len(w.launches.starts))
					}
					return
				}
				want := append([]string{"box"}, s.want...)
				want[3] = boxRepo
				if !reflect.DeepEqual(r.ran, [][]string{want}) || len(w.launches.starts) != 0 || len(o.Goals) != 0 || len(o.Managers) != 0 {
					t.Fatalf("ran %v, starts %d, goals %d, managers %d; want only %v", r.ran, len(w.launches.starts), len(o.Goals), len(o.Managers), want)
				}
			})
		}
	}
}
