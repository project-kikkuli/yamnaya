package tui

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// to moves the cursor down until the selected row passes want.
func to(t *testing.T, m Model, want func(row) bool) Model {
	t.Helper()
	for range 30 {
		if r, ok := m.selected(); ok && want(r) {
			return m
		}
		m = press(t, m, "down")
	}
	t.Fatalf("no such row on:\n%s", m.View())
	return m
}

func mission(id string) func(row) bool {
	return func(r row) bool {
		return r.project != nil && r.session == nil && r.item == nil && len(r.project.Sessions) > 0 && r.project.Sessions[0].ID == id
	}
}

func isSession(id string) func(row) bool {
	return func(r row) bool { return r.session != nil && r.member == nil && r.session.ID == id }
}

func isMember(r row) bool { return r.member != nil }

// signals makes Kill signal no process: each SIGTERM ends the fake pid unless it is stubborn.
func signals(c *core.Core, stubborn bool) *[]int {
	var sent []int
	dead := map[int]bool{}
	procs := c.Procs
	c.KillGrace = 100 * time.Millisecond
	c.Procs = func() ([]proc.Proc, error) {
		ps, err := procs()
		var live []proc.Proc
		for _, p := range ps {
			if !dead[p.PID] {
				live = append(live, p)
			}
		}
		return live, err
	}
	c.Signal = func(pid int, sig syscall.Signal) error {
		sent = append(sent, pid)
		dead[pid] = !stubborn
		return nil
	}
	return &sent
}

func TestKillAsksFirstAndSignalsOnlyOnYes(t *testing.T) {
	for _, c := range []struct {
		name     string
		at       []func(row) bool
		keys     []string
		stubborn bool
		sent     []int
		message  string
	}{
		{"n cancels", []func(row) bool{mission("lead")}, []string{"K", "n"}, false, nil, "cancelled"},
		{"esc cancels", []func(row) bool{mission("lead")}, []string{"K", "esc"}, false, nil, "cancelled"},
		{"mission with one session", []func(row) bool{mission("lead")}, []string{"K", "y"}, false, []int{1}, "killed claude lead"},
		{"session row", []func(row) bool{mission("lead"), nil, isSession("lead")}, []string{"K", "y"}, false, []int{1}, "killed claude lead"},
		{"agent row", []func(row) bool{mission("lead"), nil, isMember}, []string{"K", "y"}, false, []int{1}, "killed claude lead"},
		{"every process of the session", []func(row) bool{mission("twin")}, []string{"K", "y"}, false, []int{2, 3}, "killed claude twin"},
		{"still running", []func(row) bool{mission("lead")}, []string{"K", "y"}, true, []int{1}, "still running (pid 1)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, core, _ := setup(t)
			sent := signals(core, c.stubborn)
			for _, at := range c.at {
				if at == nil {
					m = press(t, m, "enter")
					continue
				}
				m = to(t, m, at)
			}
			m = press(t, m, c.keys[0])
			if m.mode != modeConfirm || !strings.Contains(m.View(), "kill claude ") || !strings.Contains(m.View(), "? y/n") {
				t.Fatalf("K should ask y/n first:\n%s", m.View())
			}
			m = press(t, m, c.keys[1:]...)
			if !slices.Equal(*sent, c.sent) {
				t.Fatalf("signalled %v, want %v", *sent, c.sent)
			}
			if !strings.Contains(m.message, c.message) {
				t.Fatalf("message %q, want %q", m.message, c.message)
			}
		})
	}
}

type fixture struct {
	m      Model
	c      *core.Core
	l      *launches
	ported []string
}

func TestBoardKeysRunTheSameCoreActionsAsTheirCommands(t *testing.T) {
	cwd, _ := os.Getwd()
	for _, c := range []struct {
		name  string
		at    []func(row) bool
		keys  []string
		check func(t *testing.T, f fixture) string
	}{
		{"n starts a session, tab picks codex", nil, []string{"n", "fix", " ", "login", "tab", "enter", "enter", "fix-x", "enter"}, func(t *testing.T, f fixture) string {
			want := core.NewLaunch{Prompt: "fix login", Harness: "codex", Repo: cwd, Slug: "fix-x"}
			if len(f.l.started) != 1 || f.l.started[0] != want || f.m.message != "started codex fix-x" {
				return "started " + fmtLaunches(f.l.started) + " message " + f.m.message
			}
			return ""
		}},
		{"n with esc starts nothing", nil, []string{"n", "fix", "esc"}, func(t *testing.T, f fixture) string {
			if len(f.l.started) != 0 {
				return "started " + fmtLaunches(f.l.started)
			}
			return ""
		}},
		{"L lands the session once confirmed", []func(row) bool{mission("lead"), nil, isSession("lead")}, []string{"L", "y"}, func(t *testing.T, f fixture) string {
			if !slices.Equal(f.l.landed, []string{"lead"}) || f.m.message != "landed lead" {
				return "landed " + strings.Join(f.l.landed, ",") + " message " + f.m.message
			}
			return ""
		}},
		{"L then n lands nothing", []func(row) bool{mission("lead"), nil, isSession("lead")}, []string{"L", "n"}, func(t *testing.T, f fixture) string {
			if len(f.l.landed) != 0 {
				return "landed " + strings.Join(f.l.landed, ",")
			}
			return ""
		}},
		{"T ports the session to the other harness", []func(row) bool{mission("lead"), nil, isSession("lead")}, []string{"T", "y"}, func(t *testing.T, f fixture) string {
			if !slices.Equal(f.ported, []string{"lead codex"}) || !strings.HasPrefix(f.m.message, "ported to codex session lead2") {
				return "ported " + strings.Join(f.ported, ",") + " message " + f.m.message
			}
			return ""
		}},
		{"P sets phases, p advances them", []func(row) bool{mission("lead")}, []string{"P", "plan, build, ship", "enter", "p", "y"}, func(t *testing.T, f fixture) string {
			ph, err := f.c.Store.Phases(projectKey(f, "lead"))
			if err != nil || !slices.Equal(ph.Names, []string{"plan", "build", "ship"}) || ph.Current != 1 {
				return fmt.Sprintf("phases %v at %d, message %s", ph.Names, ph.Current, f.m.message)
			}
			return ""
		}},
		{"v sets the vibe", []func(row) bool{mission("lead")}, []string{"v", "Hawaii: tests green", "enter"}, func(t *testing.T, f fixture) string {
			vs, err := f.c.Store.Vibes()
			if err != nil || len(vs) != 1 || vs[0].Place != "Hawaii" || vs[0].Why != "tests green" || vs[0].Target != projectKey(f, "lead") {
				return "vibes " + fmtVibes(vs) + " message " + f.m.message
			}
			return ""
		}},
		{"S with no merges says so", []func(row) bool{mission("lead")}, []string{"S"}, func(t *testing.T, f fixture) string {
			if !strings.Contains(f.m.message, "has no merges") {
				return "message " + f.m.message
			}
			return ""
		}},
		{"I lists answered items too", nil, []string{"a", "enter", "I"}, func(t *testing.T, f fixture) string {
			if v := f.m.View(); f.m.mode != modeItems || !strings.Contains(v, "answered") || !strings.Contains(v, "Rename the flag?") {
				return "items view:\n" + v
			}
			return ""
		}},
		{"U shows spend and limits", nil, []string{"U"}, func(t *testing.T, f fixture) string {
			if v := f.m.View(); f.m.mode != modeScreen || !strings.Contains(v, "spend on") || !strings.Contains(v, "limit: claude") {
				return "usage view:\n" + v
			}
			return ""
		}},
		{"E lists launched sessions", nil, []string{"E"}, func(t *testing.T, f fixture) string {
			if v := f.m.View(); f.m.mode != modeScreen || !strings.Contains(v, "fix-login") || !strings.Contains(v, "exited 1") {
				return "launched view:\n" + v
			}
			return ""
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			f := fixture{c: k, l: &launches{}}
			k.Launches = f.l
			k.Port = func(cwd, id, to string) (string, error) {
				f.ported = append(f.ported, id+" "+to)
				return id + "2", nil
			}
			now := time.Now()
			k.Meter = meter{core.Fleet{Ready: true, At: now, Limits: []core.Limit{{Harness: "claude", Name: "5h", Used: 30, Window: 5 * time.Hour, ResetsAt: now.Add(time.Hour), Seen: now}}}}
			m = refresh(t, m)
			for _, at := range c.at {
				if at == nil {
					m = press(t, m, "enter")
					continue
				}
				m = to(t, m, at)
			}
			for _, key := range c.keys {
				if len(key) > 1 && key != "enter" && key != "esc" && key != "tab" {
					m = press(t, m, strings.Split(key, "")...)
					continue
				}
				m = press(t, m, key)
			}
			f.m = m
			if bad := c.check(t, f); bad != "" {
				t.Fatal(bad)
			}
		})
	}
}

func projectKey(f fixture, session string) string { return f.m.board.ProjectOf(session) }

func fmtLaunches(ls []core.NewLaunch) string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Harness+" "+l.Repo+" "+l.Slug+" "+l.Prompt)
	}
	return strings.Join(out, "; ")
}

func fmtVibes(vs []core.Vibe) string {
	var out []string
	for _, v := range vs {
		out = append(out, v.Place+" "+v.Why+" "+v.Target)
	}
	return strings.Join(out, "; ")
}
