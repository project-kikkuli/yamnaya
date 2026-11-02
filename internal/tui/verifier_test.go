package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestVerifierIsReachableInGoalDetailOutsideOwnerSessions(t *testing.T) {
	for _, state := range []string{core.GoalVerifying, core.GoalDone} {
		for _, managerLive := range []bool{true, false} {
			name := state + "/manager absent"
			if managerLive {
				name = state + "/manager present"
			}
			t.Run(name, func(t *testing.T) {
				m, c, _ := setup(t)
				at := time.Date(2026, 10, 30, 23, 55, 11, 0, time.UTC)
				c.Now = func() time.Time { return at }
				g, err := c.Store.AddGoal(core.Goal{Ask: "check importer", Repo: "/repo", CreatedAt: at})
				if err != nil {
					t.Fatal(err)
				}
				mg, err := c.Store.AddManager(core.Manager{Role: core.RoleManager, GoalID: g.ID, Name: "importer", LaunchedAt: at})
				if err != nil {
					t.Fatal(err)
				}
				mg.Session = "lead"
				if err := c.Store.SaveManager(mg); err != nil {
					t.Fatal(err)
				}
				g.ManagerID = mg.ID
				if err := c.Store.SaveGoal(g); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Store.AddVerification(core.Verification{Goal: g.ID, Session: "twin"}); err != nil {
					t.Fatal(err)
				}
				g, err = c.Store.Goal(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				g.State, g.DoneAt = state, at
				if err = c.Store.SaveGoal(g); err != nil {
					t.Fatal(err)
				}
				g.Version++
				if !managerLive {
					mg.Session = "absent-manager"
					if err := c.Store.SaveManager(mg); err != nil {
						t.Fatal(err)
					}
				}
				m = refresh(t, m)
				for _, r := range m.rows() {
					if r.project != nil && r.project.Key == "twin" {
						t.Fatal("verifier listed as owner session")
					}
				}
				if _, ok := m.board.Project("twin"); !ok {
					t.Fatal("verifier project removed")
				}
				m.mode, m.goal = modeGoal, mg.ID
				next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
				m = next.(Model)
				count := 0
				for _, r := range m.rows() {
					if r.session != nil && r.session.Key() == "twin" {
						count++
						if !r.verifier {
							t.Fatal("verifier role missing")
						}
					}
				}
				if count != 1 {
					t.Fatalf("verifier visible %d times", count)
				}
				if content := ansi.Strip(m.View().Content); !strings.Contains(content, "verifier") || !strings.Contains(content, "Twin work") {
					t.Fatalf("goal detail missing role/session:\n%s", content)
				}
				n, _ := m.board.Node(mg.ID)
				for _, a := range n.Agents {
					if a.Key() == "twin" {
						t.Fatal("verifier counted as ordinary agent")
					}
				}
				for _, w := range n.Workers {
					if w.Session == "twin" {
						t.Fatal("verifier counted as ordinary worker")
					}
				}
				c.Launches = &launches{dormant: []core.Dormant{{Key: "verify-importer", Session: "twin", Harness: "claude", Title: "Finished verifier", State: "exited 0"}}}
				c.Procs = func() ([]proc.Proc, error) {
					return []proc.Proc{{PID: 1, TTY: "/dev/a", Args: []string{"claude"}}}, nil
				}
				m = refresh(t, m)
				count = 0
				for _, r := range m.rows() {
					if r.dormant != nil && r.dormant.Session == "twin" {
						count++
						if !r.verifier {
							t.Fatal("dormant verifier role missing")
						}
					}
				}
				if count != 1 {
					t.Fatalf("dormant verifier visible %d times", count)
				}
				if content := ansi.Strip(m.View().Content); !strings.Contains(content, "verifier") || !strings.Contains(content, "Finished verifier") {
					t.Fatalf("dormant goal detail missing session:\n%s", content)
				}
				if p, ok := m.board.Project("twin"); !ok || p.Verifier == nil || len(p.Dormant) != 1 {
					t.Fatalf("dormant verifier project lost: %+v", p)
				}
				if state == core.GoalDone {
					g.AckedAt = at
					if err := c.Store.SaveGoal(g); err != nil {
						t.Fatal(err)
					}
					m = refresh(t, m)
					if _, ok := m.board.Node(mg.ID); ok {
						t.Fatal("acknowledged goal kept visible")
					}
					g.Version++
					g.AckedAt, g.DoneAt = time.Time{}, at.Add(-core.DoneShown)
					if err := c.Store.SaveGoal(g); err != nil {
						t.Fatal(err)
					}
					m = refresh(t, m)
					if _, ok := m.board.Node(mg.ID); ok {
						t.Fatal("aged goal kept visible")
					}
				}
			})
		}
	}
}

func TestVerifierGoalDetailOpensTheRightHostForACollidingUUID(t *testing.T) {
	m, c, _ := setup(t)
	at := time.Now()
	r := core.RemoteBoard{Host: "box", At: at,
		Sessions:         []core.Live{{Session: core.Session{ID: "twin", Harness: "claude", Title: "Remote verifier", Status: core.Busy}, Remote: "box", Project: "twin"}},
		Managers:         []core.Manager{{ID: "mgr1", Role: core.RoleManager, GoalID: "goal46", Session: "remote-manager"}},
		Goals:            []core.Goal{{ID: "goal46", ManagerID: "mgr1", State: core.GoalVerifying, Ask: "verify the importer"}},
		VerifierSessions: map[string]core.VerifierSession{"twin": {Verification: 20, Goal: "goal46"}},
	}
	c.Remotes, c.ShowRemotes = &remotes{boards: []core.RemoteBoard{r}}, true
	m = refresh(t, m)
	owner := 0
	for _, r := range m.rows() {
		if r.project != nil && r.project.Key == "twin" {
			owner++
			if r.project.Verifier != nil {
				t.Fatal("remote verifier appears under owner")
			}
		}
	}
	if owner != 1 {
		t.Fatalf("local owner row count %d", owner)
	}
	m.mode, m.goal = modeGoal, "box/mgr1"
	rows := m.rows()
	if len(rows) != 1 || !rows[0].verifier || rows[0].session.Remote != "box" {
		t.Fatalf("goal rows=%+v", rows)
	}
	m.cursor = 0
	m = press(t, m, "enter")
	s, ok := m.session()
	if !ok || s.Remote != "box" || s.Title != "Remote verifier" {
		t.Fatalf("opened the wrong host: %+v", s)
	}
}
