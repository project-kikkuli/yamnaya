package tui

import (
	"errors"
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

// to selects the first visible row that passes want.
func to(t *testing.T, m Model, want func(row) bool) Model {
	t.Helper()
	m = press(t, m, "home")
	for range len(m.rows()) + 1 {
		if r, ok := m.selected(); ok && want(r) {
			return m
		}
		m = press(t, m, "down")
	}
	t.Fatalf("no such row on:\n%s", m.View().Content)
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
			if m.mode != modeConfirm || !strings.Contains(m.View().Content, "kill claude ") || !strings.Contains(m.View().Content, "? y/n") {
				t.Fatalf("K should ask y/n first:\n%s", m.View().Content)
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
		{"+ starts a session once the prompt is typed, tab picks codex", nil, []string{"+", "fix", " ", "login", "tab", "enter"}, func(t *testing.T, f fixture) string {
			want := core.NewLaunch{Prompt: "fix login", Harness: "codex", Repo: core.MainCheckout(cwd)}
			if len(f.l.started) != 1 || f.l.started[0] != want || !strings.HasPrefix(f.m.message, "started codex") {
				return "started " + fmtLaunches(f.l.started) + " message " + f.m.message
			}
			return ""
		}},
		{"+ with esc starts nothing", nil, []string{"+", "fix", "esc"}, func(t *testing.T, f fixture) string {
			if len(f.l.started) != 0 {
				return "started " + fmtLaunches(f.l.started)
			}
			return ""
		}},
		{"K ends the launched session, its pane and worktree once confirmed", []func(row) bool{mission("lead"), nil, isSession("lead")}, []string{"K", "y"}, func(t *testing.T, f fixture) string {
			if !slices.Equal(f.l.landed, []string{"lead"}) || f.m.message != "killed lead" || len(f.m.killing) != 0 {
				return "ended " + strings.Join(f.l.landed, ",") + " message " + f.m.message
			}
			return ""
		}},
		{"K then n kills nothing", []func(row) bool{mission("lead"), nil, isSession("lead")}, []string{"K", "n"}, func(t *testing.T, f fixture) string {
			if len(f.l.landed) != 0 {
				return "ended " + strings.Join(f.l.landed, ",")
			}
			return ""
		}},
		{"G sweeps worktrees once confirmed", nil, []string{"G", "y"}, func(t *testing.T, f fixture) string {
			if f.l.swept != 1 || f.m.message != "removed 1 worktrees" {
				return fmt.Sprintf("swept %d times, message %s", f.l.swept, f.m.message)
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
		{"I lists answered items too", nil, []string{"r", "enter", "I"}, func(t *testing.T, f fixture) string {
			if v := f.m.View().Content; f.m.mode != modeItems || !strings.Contains(v, "answered") || !strings.Contains(v, "Rename the flag?") {
				return "items view:\n" + v
			}
			return ""
		}},
		{"U shows spend and limits", nil, []string{"U"}, func(t *testing.T, f fixture) string {
			if v := f.m.View().Content; f.m.mode != modeScreen || !strings.Contains(v, "spend on") || !strings.Contains(v, "limit: claude") {
				return "usage view:\n" + v
			}
			return ""
		}},
		{"E lists launched sessions", nil, []string{"E"}, func(t *testing.T, f fixture) string {
			if v := f.m.View().Content; f.m.mode != modePanel || !strings.Contains(v, "fix-login") || !strings.Contains(v, "exited 1") {
				return "launched view:\n" + v
			}
			return ""
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			f := fixture{c: k, l: &launches{}}
			k.Launches = f.l
			signals(k, false)
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

// placed runs the fixture's sessions in /src/app-wt of repository /src/app, with session 1 on a permission prompt.
type placed struct{ core.Harness }

func (h placed) Discover(procs []proc.Proc) []core.Session {
	out := h.Harness.Discover(procs)
	for i := range out {
		out[i].Cwd = "/src/app-wt"
		if out[i].PID == 1 {
			out[i].Status, out[i].Prompt = core.Waiting, &core.Prompt{Tool: "Bash", Detail: "rm -rf build"}
		}
	}
	return out
}

func place(t *testing.T, m Model, c *core.Core) Model {
	c.Harnesses[0] = placed{c.Harnesses[0]}
	c.Git = func(string) core.GitInfo { return core.GitInfo{Common: "/src/app/.git", Top: "/src/app-wt"} }
	m = refresh(t, m)
	m = press(t, m, "home", "down")
	return m
}

func TestKillWorksOnAPermissionPromptButNotOnAnItem(t *testing.T) {
	for _, c := range []struct {
		name string
		at   func(row) bool
		sent []int
	}{
		{"permission prompt", func(r row) bool { return r.need != nil && r.item == nil }, []int{1}},
		{"open item", func(r row) bool { return r.need != nil && r.item != nil }, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			m = place(t, m, k)
			sent := signals(k, false)
			m = press(t, to(t, m, c.at), "K")
			if asks := m.mode == modeConfirm; asks != (c.sent != nil) {
				t.Fatalf("K asked %v:\n%s", asks, m.View().Content)
			}
			m = press(t, m, "y")
			if !slices.Equal(*sent, c.sent) {
				t.Fatalf("signalled %v, want %v", *sent, c.sent)
			}
		})
	}
}

func TestNewStartsInTheSelectedRowsRepoAndShowsFailuresInline(t *testing.T) {
	cwd, _ := os.Getwd()
	for _, c := range []struct {
		name    string
		at      func(row) bool
		keys    []string
		err     error
		repo    string
		message string
	}{
		{"mission row", mission("lead"), []string{"+", "go", "enter"}, nil, "/src/app", "started claude "},
		{"repo row", func(r row) bool { return r.repo != nil && r.project == nil }, []string{"+", "go", "enter"}, nil, "/src/app", "started claude "},
		{"permission prompt", func(r row) bool { return r.need != nil && r.item == nil }, []string{"+", "go", "enter"}, nil, "/src/app", "started claude "},
		{"item with no repo", func(r row) bool { return r.need != nil && r.item != nil }, []string{"+", "go", "enter"}, nil, core.MainCheckout(cwd), "started claude "},
		{"not a git repo", mission("lead"), []string{"+", "go", "enter"}, errors.New("/src/app is not a git repository"), "/src/app", "error: /src/app is not a git repository"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			l := &launches{startErr: c.err}
			k.Launches = l
			m = to(t, place(t, m, k), c.at)
			for _, key := range c.keys {
				if len(key) > 1 && key != "enter" {
					m = press(t, m, strings.Split(key, "")...)
					continue
				}
				m = press(t, m, key)
			}
			if len(l.started) != 1 || l.started[0].Repo != c.repo || l.started[0].Harness != "claude" || l.started[0].Prompt != "go" {
				t.Fatalf("started %s, want claude in %s", fmtLaunches(l.started), c.repo)
			}
			if !strings.HasPrefix(m.message, c.message) || m.mode != modeList {
				t.Fatalf("message %q in mode %v, want %q on the list", m.message, m.mode, c.message)
			}
		})
	}
}

func TestBoardKeysWaitWhileAMergeIsPicked(t *testing.T) {
	for _, key := range []string{"?", "h", "n", "N", "I", "U", "E", "K", "p", "O"} {
		t.Run(key, func(t *testing.T) {
			m, k, _ := setup(t)
			k.Launches = &launches{}
			m = press(t, to(t, m, mission("lead")), "m", key)
			if m.mode != modeMerge {
				t.Fatalf("%s left the merge for mode %v", key, m.mode)
			}
			if m = press(t, m, "esc"); m.mode != modeList || m.message != "merge cancelled" {
				t.Fatalf("esc after %s: mode %v message %q", key, m.mode, m.message)
			}
		})
	}
}

// rooted runs the fixture's sessions in /src/w.
type rooted struct{ core.Harness }

func (h rooted) Discover(procs []proc.Proc) []core.Session {
	out := h.Harness.Discover(procs)
	for i := range out {
		out[i].Cwd = "/src/w"
	}
	return out
}

// codexResumes stands in for Codex only to say how to resume a Codex session.
type codexResumes struct{ core.Harness }

func (codexResumes) Name() string                        { return "codex" }
func (codexResumes) Owns(proc.Proc) bool                 { return false }
func (codexResumes) Discover([]proc.Proc) []core.Session { return nil }
func (codexResumes) Resume(id string) []string           { return []string{"codex", "resume", id} }

func TestTEndsTheSessionPortsItAndRelaunchesItInTheOtherHarness(t *testing.T) {
	for _, c := range []struct {
		name      string
		keys      []string
		stubborn  bool
		portErr   error
		launchErr error
		sent      []int
		ported    []string
		relaunch  []string
		message   string
	}{
		{"y relaunches in the same directory", []string{"y"}, false, nil, nil, []int{1}, []string{"/src/w lead codex"}, []string{"lead codex lead2 /src/w"}, "relaunched lead as codex lead2: revived adopt-lead2"},
		{"n cancels", []string{"n"}, false, nil, nil, nil, nil, nil, "cancelled"},
		{"esc cancels", []string{"esc"}, false, nil, nil, nil, nil, nil, "cancelled"},
		{"still running after SIGTERM", []string{"y"}, true, nil, nil, []int{1}, nil, nil, "still running (pid 1) after SIGTERM; not relaunched"},
		{"port fails", []string{"y"}, false, errors.New("cs broke"), nil, []int{1}, []string{"/src/w lead codex"}, nil, "porting failed: cs broke; resume it with: cd /src/w && claude --resume lead"},
		{"launch fails", []string{"y"}, false, nil, errors.New("no tmux"), []int{1}, []string{"/src/w lead codex"}, []string{"lead codex lead2 /src/w"}, "starting it failed: no tmux; resume it with: cd /src/w && codex resume lead2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, k, _ := setup(t)
			k.Harnesses = []core.Harness{rooted{k.Harnesses[0]}, codexResumes{k.Harnesses[0]}}
			sent := signals(k, c.stubborn)
			l := &launches{relaunchErr: c.launchErr}
			k.Launches = l
			var ported []string
			k.Port = func(cwd, id, to string) (string, error) {
				ported = append(ported, cwd+" "+id+" "+to)
				return id + "2", c.portErr
			}
			m = refresh(t, m)
			m = to(t, m, mission("lead"))
			m = press(t, m, "enter")
			m = to(t, m, isSession("lead"))
			m = press(t, m, "T")
			if v := m.View().Content; m.mode != modeConfirm || !strings.Contains(v, "end claude lead (Lead work) and relaunch it in codex? y/n") {
				t.Fatalf("T should ask y/n first:\n%s", v)
			}
			m = press(t, m, c.keys...)
			if !slices.Equal(*sent, c.sent) || !slices.Equal(ported, c.ported) || !slices.Equal(l.relaunched, c.relaunch) {
				t.Fatalf("signalled %v ported %v relaunched %v; want %v %v %v", *sent, ported, l.relaunched, c.sent, c.ported, c.relaunch)
			}
			if !strings.Contains(m.message, c.message) {
				t.Fatalf("message %q, want %q", m.message, c.message)
			}
		})
	}
}
