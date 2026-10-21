package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestSessionControlsStayVisibleAndOpenTheAdvertisedAction(t *testing.T) {
	for _, width := range []int{40, 79, 80, 160} {
		for _, where := range []string{"board mission", "session row", "session detail", "exited launch", "mission only", "launcher unavailable", "several live sessions", "repository"} {
			t.Run(fmt.Sprintf("%s/%d", where, width), func(t *testing.T) {
				m, c, _ := setup(t)
				c.Launches = &launches{}
				wantKill, wantLand, wantArchive := false, false, false
				switch where {
				case "board mission":
					m = to(t, m, mission("lead"))
					wantKill, wantLand = true, true
				case "session row":
					m = to(t, m, mission("lead"))
					m = press(t, m, "enter")
					m = to(t, m, isSession("lead"))
					wantKill, wantLand = true, true
				case "session detail":
					m = to(t, m, mission("lead"))
					m = press(t, m, "enter")
					m = to(t, m, isSession("lead"))
					m = press(t, m, "enter")
					wantKill, wantLand = true, true
				case "exited launch":
					c.Launches = &launches{dormant: []core.Dormant{{Key: "finished", Harness: "claude", Session: "old", State: "exited 0"}}}
					m = refresh(t, m)
					m = to(t, m, func(r row) bool {
						return r.project != nil && len(r.project.Dormant) > 0 && len(r.project.Sessions) == 0
					})
					wantLand = true
				case "mission only":
					var wID string
					if err := c.ObserveWork(m.board); err != nil {
						t.Fatal(err)
					}
					all, _ := c.Store.Work.List()
					wID = all[0].ID
					c.Procs = func() ([]proc.Proc, error) { return nil, nil }
					c.Harnesses = nil
					m = refresh(t, m)
					m = to(t, m, func(r row) bool {
						return r.project != nil && r.project.Work != nil && r.project.Work.ID == wID && r.item == nil && r.need == nil
					})
					wantArchive = true
				case "launcher unavailable":
					c.Launches = nil
					m = to(t, m, mission("lead"))
					m = press(t, m, "enter")
					m = to(t, m, isSession("lead"))
					wantKill = true
				case "several live sessions":
					if err := c.Store.Link("twin", "lead"); err != nil {
						t.Fatal(err)
					}
					m = refresh(t, m)
					m = to(t, m, mission("lead"))
				case "repository":
					m = to(t, m, func(r row) bool { return r.repo != nil && r.project == nil })
				}
				next, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
				m = next.(Model)
				footer := ansi.Strip(strings.Join(m.footer(), "\n"))
				rendered := ansi.Strip(m.View())
				for _, check := range []struct {
					hint string
					want bool
				}{{"K kill", wantKill}, {"L land", wantLand}, {"L archive", wantArchive}, {"F forget", wantLand}} {
					if strings.Contains(footer, check.hint) != check.want || strings.Contains(rendered, check.hint) != check.want {
						t.Fatalf("%s present=%v want=%v:\n%s", check.hint, strings.Contains(footer, check.hint), check.want, footer)
					}
				}
				for _, action := range []struct {
					key, label string
					want       bool
				}{{"K", "kill ", wantKill}, {"L", "land ", wantLand}, {"L", "archive ", wantArchive}} {
					if !action.want {
						continue
					}
					next := press(t, m, action.key)
					if next.mode != modeConfirm || !strings.Contains(next.ask, action.label) {
						t.Fatalf("advertised %s did not open %s confirmation: %s", action.key, action.label, next.View())
					}
				}
			})
		}
	}
}
