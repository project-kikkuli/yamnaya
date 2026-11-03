package tui

import (
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type terminalRemotes struct {
	remotes
	attached [][]string
}

func (r *terminalRemotes) Attach(host string, args ...string) *exec.Cmd {
	r.attached = append(r.attached, append([]string{host}, args...))
	return exec.Command("false")
}

func TestTerminalOpenKeepsSelectedHostThroughEveryView(t *testing.T) {
	for _, verifier := range []bool{false, true} {
		for _, copies := range []int{1, 2} {
			for _, view := range []string{"mission", "detail", "session", "item"} {
				for _, key := range []string{"o", "O"} {
					t.Run(fmt.Sprintf("verifier=%v/copies=%d/%s/%s", verifier, copies, view, key), func(t *testing.T) {
						m, c, h := setup(t)
						if verifier {
							g, err := c.Store.AddGoal(core.Goal{Ask: "check local work", Repo: t.TempDir()})
							if err != nil {
								t.Fatal(err)
							}
							if _, err := c.Store.AddVerification(core.Verification{Goal: g.ID, Session: "twin"}); err != nil {
								t.Fatal(err)
							}
						}
						r := &terminalRemotes{remotes: remotes{boards: []core.RemoteBoard{{Host: "box", At: time.Now(),
							Items: []core.Item{{Host: "box", Seq: 77, ID: "D77", Kind: core.KindDecision, Minutes: 1, Session: "twin", State: core.ItemOpen, Question: "Remote question"}},
						}}}}
						for i := range copies {
							r.boards[0].Sessions = append(r.boards[0].Sessions, core.Live{Session: core.Session{
								ID: "twin", Harness: "claude", Title: "Remote owner", PID: 20 + i, TTY: fmt.Sprintf("/dev/remote%d", i), Status: core.Busy,
							}, Project: "twin"})
						}
						c.Remotes, c.ShowRemotes = r, true
						m.peaks.hidden = true
						m = refresh(t, m)
						if view == "item" {
							m = press(t, m, "home")
							m = to(t, m, func(r row) bool { return r.item != nil && r.item.Host == "box" })
						} else {
							m = to(t, m, projectOnHost("twin", "box"))
							if view != "mission" {
								m = press(t, m, "enter")
							}
							if view == "session" {
								m = press(t, m, "enter")
							}
						}
						m, cmd := step(t, m, keyOf(key))
						if copies > 1 {
							if m.mode != modePick || len(m.targets) != copies {
								t.Fatalf("remote copies missing from chooser: mode=%v targets=%+v", m.mode, m.targets)
							}
							for _, target := range m.targets {
								if target.Remote != "box" || target.Session.Title != "Remote owner" || target.Session.PID < 20 {
									t.Fatalf("chooser contains local collision: %+v", target)
								}
							}
							m = press(t, m, "down")
							m, cmd = step(t, m, keyOf("enter"))
						} else if m.mode == modePick {
							t.Fatalf("single remote acquired local copies: %+v", m.targets)
						}
						if cmd == nil {
							t.Fatalf("open dispatched nothing: %q", m.message)
						}
						if key == "o" {
							if len(r.attached) != 1 || r.attached[0][0] != "box" || !strings.Contains(strings.Join(r.attached[0], " "), "claude:default@twin") {
								t.Fatalf("attach crossed hosts: %v", r.attached)
							}
							if copies > 1 && !strings.HasSuffix(strings.Join(r.attached[0], " "), "--pick 2") {
								t.Fatalf("attach lost chosen remote copy: %v", r.attached)
							}
						} else {
							result := cmd().(doneMsg)
							if result.err != nil || !strings.Contains(result.message, "box") {
								t.Fatalf("beside lost remote advice: %+v", result)
							}
						}
						if len(h.focused) != 0 || len(h.split) != 0 {
							t.Fatalf("opened a local terminal: focused=%v split=%v", h.focused, h.split)
						}
					})
				}
			}
		}
	}
}

func TestLocalTerminalChooserKeepsOnlyItsPhysicalCopies(t *testing.T) {
	for _, key := range []string{"o", "O"} {
		t.Run(key, func(t *testing.T) {
			m, c, h := setup(t)
			c.Remotes, c.ShowRemotes = &remotes{boards: []core.RemoteBoard{{Host: "box", At: time.Now(), Sessions: []core.Live{
				{Session: core.Session{ID: "twin", Harness: "claude", PID: 20, Title: "Remote owner"}, Project: "twin"},
			}}}}, true
			m.peaks.hidden = true
			m = refresh(t, m)
			m = to(t, m, projectOnHost("twin", ""))
			m, _ = step(t, m, keyOf(key))
			if m.mode != modePick || len(m.targets) != 2 {
				t.Fatalf("local copies missing: mode=%v targets=%+v", m.mode, m.targets)
			}
			for _, target := range m.targets {
				if target.Remote != "" || target.Session.PID != 2 && target.Session.PID != 3 || target.Session.Title != "Twin work" {
					t.Fatalf("local chooser crossed hosts: %+v", target)
				}
			}
			if len(h.focused) != 0 || len(h.split) != 0 {
				t.Fatal("chooser opened a terminal")
			}
		})
	}
}
