package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestAfterAKillTheCursorLandsOnTheNextRowAndKWorksAtOnce(t *testing.T) {
	names := []string{"alpha", "bravo", "charlie", "delta"}
	for victim := range names[:len(names)-1] {
		for _, asks := range []int{0, 1, 3} {
			t.Run(fmt.Sprintf("%s with %d asks", names[victim], asks), func(t *testing.T) {
				m, c, _ := setup(t)
				sessions := map[int]core.Session{}
				var procs []proc.Proc
				for i, n := range names {
					sessions[i+10] = core.Session{ID: n, Title: n + " work", Status: core.Idle}
					procs = append(procs, proc.Proc{PID: i + 10, TTY: fmt.Sprintf("/dev/t%d", i), Args: []string{"claude"}})
				}
				c.Harnesses = []core.Harness{harness{sessions}}
				c.Procs = func() ([]proc.Proc, error) { return procs, nil }
				signals(c, false)
				for _, s := range []string{"ghost", names[victim]} {
					for q := range asks {
						if _, err := c.AddItem(core.Item{Options: picks, Kind: core.KindDecision, Question: fmt.Sprintf("%s ask %d?", s, q), Rec: "yes", Session: s}); err != nil {
							t.Fatal(err)
						}
					}
				}
				m = refresh(t, m)
				m = to(t, m, mission(names[victim]))
				m = press(t, m, "K", "y")
				r, ok := m.selected()
				if want := names[victim+1]; !ok || !mission(want)(r) {
					t.Fatalf("after killing %s the cursor is on %q, want the mission of %s:\n%s", names[victim], rowKey(r), want, m.View().Content)
				}
				if footer := strings.Join(m.footer(), "\n"); !strings.Contains(footer, "K kill") {
					t.Fatalf("K is not offered on the next row:\n%s", footer)
				}
				if next := press(t, m, "K"); next.mode != modeConfirm || !strings.Contains(next.ask, names[victim+1]) {
					t.Fatalf("K on the next row did not ask to kill %s: %q", names[victim+1], next.ask)
				}
			})
		}
	}
}
