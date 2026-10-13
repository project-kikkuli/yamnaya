package cli

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/project-kikkuli/yamnaya/internal/discover"
	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
)

// newHomeEnv runs the home window's sidebar and rest panes as plain sleepers so the test drives the board model itself.
func newHomeEnv(t *testing.T) *testEnv {
	e := newTestEnv(t)
	stand := filepath.Join(e.dir, "ym-stand-in")
	if err := os.WriteFile(stand, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	orig := ymBin
	ymBin = func() string { return stand }
	t.Cleanup(func() { ymBin = orig })
	return e
}

type paneSpot struct{ window, role string }

// spots maps each pane id to the tmux window it sits in and its ym role.
func (e *testEnv) spots() map[string]paneSpot {
	e.t.Helper()
	out := map[string]paneSpot{}
	for _, line := range strings.Split(e.tmux("list-panes", "-a", "-F", "#{pane_id} #{window_id} #{@ym_role}."), "\n") {
		f := strings.Fields(line)
		out[f[0]] = paneSpot{f[1], strings.TrimSuffix(f[2], ".")}
	}
	return out
}

func (e *testEnv) homeWindowID() string {
	return e.tmux("display-message", "-p", "-t", homeWindow, "#{window_id}")
}

func (e *testEnv) agentPane(project, window string) string {
	return e.tmux("display-message", "-p", "-t", e.target(project, window), "#{pane_id}")
}

func (e *testEnv) windowID(project, name string) int64 {
	e.t.Helper()
	w, err := e.a.store.OpenWindow(project, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return w.ID
}

func (e *testEnv) staged() (store.Stage, bool) {
	e.t.Helper()
	st, ok, err := e.a.store.Stage()
	if err != nil {
		e.t.Fatal(err)
	}
	return st, ok
}

func (e *testEnv) assertHomeResting() {
	e.t.Helper()
	panes, err := e.a.tmux.WindowPanes(homeWindow)
	if err != nil {
		e.t.Fatal(err)
	}
	roles := []string{}
	for _, p := range panes {
		roles = append(roles, p.Role)
	}
	if strings.Join(roles, ",") != "sidebar,rest" {
		e.t.Fatalf("home panes %v, want sidebar,rest", roles)
	}
	if _, ok := e.staged(); ok {
		e.t.Fatal("stage record left behind")
	}
}

func TestStageSwapsAgentPanesAndReturnsThem(t *testing.T) {
	e := newHomeEnv(t)
	e.project("alpha", "w1", "w2")
	if err := e.a.ensureHome(e.dir); err != nil {
		t.Fatal(err)
	}
	home := e.homeWindowID()
	p1, p2 := e.agentPane("alpha", "w1"), e.agentPane("alpha", "w2")
	w1, w2 := e.target("alpha", "w1"), e.target("alpha", "w2")
	pid1 := e.tmux("display-message", "-p", "-t", p1, "#{pane_pid}")

	if err := e.a.stage(e.windowID("alpha", "w1"), true); err != nil {
		t.Fatal(err)
	}
	s := e.spots()
	if s[p1].window != home || s[e.agentPane("alpha", "w1")].role != "rest" {
		t.Fatalf("after staging w1: %v", s)
	}
	if active := e.tmux("display-message", "-p", "-t", homeWindow, "#{pane_id}"); active != p1 {
		t.Errorf("stage not focused: active pane %s, want %s", active, p1)
	}
	snap, err := signals.NewReader(e.a.store, e.a.tmux).Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := snap.Project("alpha"); func() bool { w, _ := p.Window("w1"); return !w.Staged || !w.Alive || w.Pane != p1 }() {
		t.Fatalf("snapshot does not see w1 on stage: %+v", p.Windows)
	}
	if out := e.ym("ls"); !strings.Contains(out, "w1") {
		t.Fatalf("ls dropped the staged window:\n%s", out)
	}

	if err := e.a.stage(e.windowID("alpha", "w2"), false); err != nil {
		t.Fatal(err)
	}
	s = e.spots()
	if s[p1].window != w1 || s[p2].window != home || s[e.agentPane("alpha", "w2")].role != "rest" {
		t.Fatalf("after staging w2: %v", s)
	}
	if pid := e.tmux("display-message", "-p", "-t", p1, "#{pane_pid}"); pid != pid1 {
		t.Errorf("w1's process changed from %s to %s", pid1, pid)
	}

	if err := e.a.unstage(); err != nil {
		t.Fatal(err)
	}
	s = e.spots()
	if s[p1].window != w1 || s[p2].window != w2 {
		t.Fatalf("after unstage: %v", s)
	}
	e.assertHomeResting()
}

// resumedInOrigin checks that w1's own window runs its harness again, resuming its session.
func resumedInOrigin(e *testEnv, agent string) {
	e.t.Helper()
	w, err := e.a.store.OpenWindow("alpha", "w1")
	if err != nil {
		e.t.Fatal(err)
	}
	p, err := e.a.tmux.Pane(w.TmuxTarget)
	if err != nil || p.Role != "" || p.Dead {
		e.t.Fatalf("w1's window pane = %+v, %v", p, err)
	}
	if cmd := e.tmux("display-message", "-p", "-t", w.TmuxTarget, "#{pane_start_command}"); !strings.Contains(cmd, "--resume "+w.SessionID) {
		e.t.Fatalf("w1 restarted as %q, want a resume of %s", cmd, w.SessionID)
	}
	if out := e.ym("ls", "-p", "alpha"); !strings.Contains(out, "w1") {
		e.t.Fatalf("w1 row closed:\n%s", out)
	}
}

func TestStrandedStageIsReturnedOnSidebarStart(t *testing.T) {
	cases := []struct {
		name   string
		strand func(e *testEnv, agent string)
		check  func(e *testEnv, agent string)
	}{
		{"sidebar restarted", func(e *testEnv, agent string) {}, func(e *testEnv, agent string) {
			if got := e.spots()[agent].window; got != e.target("alpha", "w1") {
				e.t.Fatalf("agent in %s, want its origin", got)
			}
		}},
		{"origin window closed", func(e *testEnv, agent string) {
			e.tmux("kill-window", "-t", e.target("alpha", "w1"))
		}, func(e *testEnv, agent string) {
			target := e.target("alpha", "w1")
			if got := e.spots()[agent].window; got != target {
				e.t.Fatalf("agent in %s, want the recorded target %s", got, target)
			}
			if out := e.ym("ls", "-p", "alpha"); !strings.Contains(out, "w1") || strings.Contains(out, "w1                     worker  claude gone") {
				e.t.Fatalf("w1 not alive after recovery:\n%s", out)
			}
		}},
		{"agent pane destroyed", func(e *testEnv, agent string) {
			e.tmux("kill-pane", "-t", agent)
		}, resumedInOrigin},
		{"ym session killed", func(e *testEnv, agent string) {
			e.tmux("kill-session", "-t", homeSession)
		}, resumedInOrigin},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newHomeEnv(t)
			e.project("alpha", "w1")
			agent := e.agentPane("alpha", "w1")
			if err := e.a.ensureHome(e.dir); err != nil {
				e.t.Fatal(err)
			}
			if err := e.a.stage(e.windowID("alpha", "w1"), false); err != nil {
				e.t.Fatal(err)
			}
			c.strand(e, agent)
			newSidebar(e.a)
			c.check(e, agent)
			e.assertHomeResting()
		})
	}
}

func TestSidebarStagesInsteadOfSwitching(t *testing.T) {
	e := newHomeEnv(t)
	e.project("alpha", "w1")
	e.item("alpha", "w1", store.KindDecision)
	e.tmux("new-session", "-d", "-s", "elsewhere", "sleep 600")
	e.attach("elsewhere")
	m := newSidebar(e.a)
	m.refresh()
	agent, manager := e.agentPane("alpha", "w1"), e.agentPane("alpha", "manager")
	if cmd := press(m, "o", "g"); cmd != nil {
		t.Fatal("g closed the sidebar")
	}
	if st, ok := e.staged(); !ok || st.Pane != agent {
		t.Fatalf("stage = %+v, %v; want w1's pane %s", st, ok, agent)
	}
	if got := e.clientWindow(); got == e.target("alpha", "w1") {
		t.Fatal("sidebar switched the client instead of staging")
	}
	press(m, "esc", "down", "enter")
	if st, _ := e.staged(); st.Pane != manager {
		t.Fatalf("enter on manager staged %s", st.Pane)
	}
	if got := e.spots()[agent].window; got != e.target("alpha", "w1") {
		t.Fatalf("w1 not returned when manager took the stage: %s", got)
	}
}

func TestSidebarNewProjectFormStagesItsManager(t *testing.T) {
	e := newHomeEnv(t)
	m := newSidebar(e.a)
	m.refresh()
	press(m, "n", "gamma", "enter", "ctrl+u", e.dir, "enter", "enter", "plan,build", "enter", "go", "enter")
	p, err := e.a.store.ActiveProject("gamma")
	if err != nil {
		t.Fatal(err)
	}
	if p.Cwd != e.dir || strings.Join(p.Phases, ",") != "plan,build" {
		t.Errorf("project = %+v", p)
	}
	mgr, err := e.a.store.OpenWindow("gamma", "manager")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(mgr.Prompt, "\n\ngo") {
		t.Errorf("manager prompt %q", mgr.Prompt)
	}
	st, ok := e.staged()
	if !ok || st.WindowID != mgr.ID {
		t.Fatalf("stage = %+v, %v; want gamma's manager", st, ok)
	}
	if slot, err := e.a.stageSlot(); err != nil || slot != st.Pane {
		t.Fatalf("stage slot %s, want the manager's pane %s", slot, st.Pane)
	}
}

func TestSidebarRevivesFromKurganAndStagesManager(t *testing.T) {
	e := newHomeEnv(t)
	e.project("alpha")
	e.project("beta", "b1")
	m := newSidebar(e.a)
	if err := e.a.stage(e.windowID("beta", "b1"), false); err != nil {
		t.Fatal(err)
	}
	e.ym("land", "beta")
	e.assertHomeResting()
	m.refresh()
	press(m, "k")
	if !m.inKurgan || len(m.kurgan) != 1 || m.kurgan[0].Name != "beta" {
		t.Fatalf("kurgan = %+v", m.kurgan)
	}
	press(m, "enter")
	if m.inKurgan {
		t.Fatalf("still in kurgan: %s", m.status)
	}
	if _, err := e.a.store.ActiveProject("beta"); err != nil {
		t.Fatal(err)
	}
	if st, ok := e.staged(); !ok || st.WindowID != e.windowID("beta", "manager") {
		t.Fatalf("stage = %+v, %v; want beta's manager", st, ok)
	}
}

func TestSidebarKeepsSelectionAndHelpOnShortPanes(t *testing.T) {
	e := newHomeEnv(t)
	for _, p := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7", "p8"} {
		e.project(p)
	}
	m := newSidebar(e.a)
	m.refresh()
	m.Update(tea.WindowSizeMsg{Width: 30, Height: 8})
	for i := 0; i < 20; i++ {
		press(m, "down")
	}
	view := strings.Split(m.View(), "\n")
	if len(view) > 8 {
		t.Fatalf("view has %d lines for an 8-line pane:\n%s", len(view), strings.Join(view, "\n"))
	}
	joined := strings.Join(view, "\n")
	for _, want := range []string{"p8", "› manager", "n new  k kurgan"} {
		if !strings.Contains(joined, want) {
			t.Errorf("view lacks %q:\n%s", want, joined)
		}
	}
}

func TestStagingAPaneWithoutAYmWindow(t *testing.T) {
	cases := []struct {
		name   string
		strand func(e *testEnv, agent, origin string)
		check  func(e *testEnv, agent, origin, neighbor string)
	}{
		{"returned to its window", func(e *testEnv, agent, origin string) {}, func(e *testEnv, agent, origin, neighbor string) {
			if s := e.spots(); s[agent].window != origin || s[neighbor].window != origin {
				e.t.Fatalf("panes not back in %s: %v", origin, s)
			}
		}},
		{"origin window closed", func(e *testEnv, agent, origin string) {
			e.tmux("kill-window", "-t", origin)
		}, func(e *testEnv, agent, origin, neighbor string) {
			if got := e.tmux("display-message", "-p", "-t", agent, "#{session_name}"); got != "scratch" {
				e.t.Fatalf("pane landed in session %q, want scratch", got)
			}
		}},
		{"agent exited on stage", func(e *testEnv, agent, origin string) {
			e.tmux("kill-pane", "-t", agent)
		}, func(e *testEnv, agent, origin, neighbor string) {
			for id, spot := range e.spots() {
				if spot.window == origin && id != neighbor {
					e.t.Fatalf("origin still holds %s (%s)", id, spot.role)
				}
			}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newHomeEnv(t)
			e.project("alpha")
			e.tmux("new-session", "-d", "-s", "scratch", "sleep 600")
			e.tmux("new-window", "-d", "-t", "scratch:", "sleep 600")
			origin := e.tmux("display-message", "-p", "-t", "scratch:{end}", "#{window_id}")
			agent := e.tmux("display-message", "-p", "-t", origin, "#{pane_id}")
			neighbor := e.tmux("split-window", "-d", "-t", agent, "-P", "-F", "#{pane_id}", "sleep 600")
			if err := e.a.ensureHome(e.dir); err != nil {
				t.Fatal(err)
			}
			if err := e.a.stagePane(agent, "", "", false); err != nil {
				t.Fatal(err)
			}
			if s := e.spots(); s[agent].window != e.homeWindowID() || s[neighbor].window != origin {
				t.Fatalf("after staging: %v", s)
			}
			c.strand(e, agent, origin)
			if err := e.a.unstage(); err != nil {
				t.Fatal(err)
			}
			if err := e.a.ensureHome(e.dir); err != nil {
				t.Fatal(err)
			}
			c.check(e, agent, origin, neighbor)
			e.assertHomeResting()
		})
	}
}

func TestSidebarEnterStagesADiscoveredSession(t *testing.T) {
	e := newHomeEnv(t)
	e.project("alpha")
	e.tmux("new-session", "-d", "-s", "scratch", "sleep 600")
	pane := e.tmux("display-message", "-p", "-t", "scratch:", "#{pane_id}")
	m := newSidebar(e.a)
	m.snap.Sessions = []signals.Session{{Session: discover.Session{Pane: pane, Harness: "claude", ID: "s-9", TmuxSession: "scratch"}}}
	m.sel = row{kind: rowSession, target: pane}
	if cmd := press(m, "enter"); cmd != nil {
		t.Fatal("enter closed the sidebar")
	}
	st, ok := e.staged()
	if !ok || st.Pane != pane || st.WindowID != 0 || st.Harness != "claude" || st.SessionID != "s-9" {
		t.Fatalf("stage = %+v, %v; want the discovered pane %s", st, ok, pane)
	}
	if e.spots()[pane].window != e.homeWindowID() {
		t.Fatal("discovered pane not on stage")
	}
	if err := e.a.stage(e.windowID("alpha", "manager"), false); err != nil {
		t.Fatal(err)
	}
	if got := e.tmux("display-message", "-p", "-t", pane, "#{session_name}"); got != "scratch" {
		t.Fatalf("discovered pane went to %s when the manager took the stage", got)
	}
}

func TestNavigationFollowsScreenOrder(t *testing.T) {
	for _, sidebar := range []bool{false, true} {
		t.Run(map[bool]string{false: "popup", true: "sidebar"}[sidebar], func(t *testing.T) {
			e := newHomeEnv(t)
			e.project("alpha", "w1")
			e.project("beta")
			m := newBoard(e.a)
			if sidebar {
				m = newSidebar(e.a)
			}
			m.refresh()
			m.snap.Sessions = []signals.Session{
				{Session: discover.Session{Pane: "%901", Harness: "claude", Title: "first chat"}},
				{Session: discover.Session{Pane: "%902", Harness: "codex", Title: "second chat"}},
			}
			m.sel = row{kind: rowProject, project: "alpha"}
			lines := strings.Split(m.View(), "\n")
			at := -1
			for _, r := range m.rows() {
				label := map[rowKind]string{rowProject: r.project, rowWindow: r.window}[r.kind]
				if s, ok := m.session(r.target); ok && r.kind == rowSession {
					label = s.Title
				}
				next := -1
				for i := at + 1; i < len(lines); i++ {
					if strings.Contains(lines[i], label) {
						next = i
						break
					}
				}
				if next < 0 {
					t.Fatalf("row %q is not drawn below line %d:\n%s", label, at, strings.Join(lines, "\n"))
				}
				at = next
			}
		})
	}
}

func TestReviveRecreatesAReapedWorktree(t *testing.T) {
	for _, branchDeleted := range []bool{false, true} {
		t.Run(map[bool]string{false: "branch kept", true: "branch deleted"}[branchDeleted], func(t *testing.T) {
			e := newHomeEnv(t)
			repo := filepath.Join(e.dir, "repo")
			wt := filepath.Join(e.dir, "wt-feature")
			git := func(args ...string) string {
				t.Helper()
				out, err := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			git("init", "-q", repo)
			git("-C", repo, "commit", "-q", "--allow-empty", "-m", "init")
			git("-C", repo, "worktree", "add", "-q", "-b", "feature", wt)
			git("-C", wt, "commit", "-q", "--allow-empty", "-m", "feature work")
			head := git("-C", wt, "rev-parse", "HEAD")
			wt, _ = filepath.EvalSymlinks(wt)
			e.ym("new", "gamma", "--cwd", wt)
			e.ym("land", "gamma")
			if err := os.RemoveAll(wt); err != nil {
				t.Fatal(err)
			}
			if branchDeleted {
				git("-C", repo, "worktree", "prune")
				git("-C", repo, "branch", "-D", "feature")
			}
			e.ym("revive", "gamma")
			if got := git("-C", wt, "symbolic-ref", "--short", "HEAD"); got != "feature" {
				t.Fatalf("worktree on %q, want feature", got)
			}
			if got := git("-C", wt, "rev-parse", "HEAD"); got != head {
				t.Fatalf("worktree at %s, want %s", got, head)
			}
			if ls := e.ym("ls", "-p", "gamma"); !strings.Contains(ls, "manager") || strings.Contains(ls, "gone") {
				t.Fatalf("manager not running after revive:\n%s", ls)
			}
		})
	}
}

func TestDestroyedDiscoveredSessionResumesInItsOwnWindow(t *testing.T) {
	cases := []struct {
		name   string
		strand func(e *testEnv, agent string)
	}{
		{"pane killed", func(e *testEnv, agent string) { e.tmux("kill-pane", "-t", agent) }},
		{"ym session killed", func(e *testEnv, agent string) { e.tmux("kill-session", "-t", homeSession) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newHomeEnv(t)
			e.project("alpha")
			work := filepath.Join(e.dir, "work")
			if err := os.MkdirAll(work, 0o755); err != nil {
				t.Fatal(err)
			}
			work, _ = filepath.EvalSymlinks(work)
			e.tmux("new-session", "-d", "-s", "scratch", "-c", work, "sleep 600")
			origin := e.tmux("display-message", "-p", "-t", "scratch:", "#{window_id}")
			agent := e.tmux("display-message", "-p", "-t", origin, "#{pane_id}")
			if err := e.a.stagePane(agent, "claude", "sess-1", false); err != nil {
				t.Fatal(err)
			}
			c.strand(e, agent)
			newSidebar(e.a)
			p, err := e.a.tmux.Pane(origin)
			if err != nil || p.Role != "" || p.Dead {
				t.Fatalf("origin pane = %+v, %v", p, err)
			}
			if cmd := e.tmux("display-message", "-p", "-t", origin, "#{pane_start_command}"); !strings.Contains(cmd, "--resume sess-1") {
				t.Fatalf("origin restarted as %q", cmd)
			}
			if got := e.tmux("display-message", "-p", "-t", origin, "#{pane_current_path}"); got != work {
				t.Fatalf("resumed in %s, want %s", got, work)
			}
			e.assertHomeResting()
		})
	}
}

func TestHomeFromInsideAnotherPaneLeavesThatPaneAlone(t *testing.T) {
	e := newHomeEnv(t)
	e.tmux("new-session", "-d", "-s", "work", "sleep 600")
	caller := e.tmux("display-message", "-p", "-t", "work:", "#{pane_id}")
	t.Setenv("TMUX", "/tmp/ym-test,1,0")
	t.Setenv("TMUX_PANE", caller)
	if err := e.a.ensureHome(e.dir); err != nil {
		t.Fatal(err)
	}
	panes := e.tmux("list-panes", "-t", "work:", "-F", "#{pane_id}:#{@ym_role}:#{pane_width}")
	if panes != caller+"::"+e.tmux("display-message", "-p", "-t", "work:", "#{window_width}") {
		t.Fatalf("caller's window changed: %s", panes)
	}
	e.assertHomeResting()
	if w := e.tmux("display-message", "-p", "-t", homeWindow+".{left}", "#{pane_width}"); w != "30" {
		t.Fatalf("sidebar width %s", w)
	}
}

func TestNextInsideHomeStagesInsteadOfLeaving(t *testing.T) {
	e := newHomeEnv(t)
	e.project("alpha", "w1", "w2")
	e.item("alpha", "w2", store.KindDecision)
	e.item("alpha", "w1", store.KindDecision)
	if err := e.a.ensureHome(e.dir); err != nil {
		t.Fatal(err)
	}
	e.attach(homeSession)
	home := e.homeWindowID()
	for i, want := range []string{"w2", "w1", "w2"} {
		e.ym("next")
		if got := e.clientWindow(); got != home {
			t.Fatalf("next %d moved the client to %s", i+1, got)
		}
		if st, ok := e.staged(); !ok || st.WindowID != e.windowID("alpha", want) {
			t.Fatalf("next %d staged %+v, want %s", i+1, st, want)
		}
	}
}

func (e *testEnv) running(sessionID string) []string {
	e.t.Helper()
	panes, err := e.a.tmux.PanesRunning(sessionID)
	if err != nil {
		e.t.Fatal(err)
	}
	return panes
}

func TestSwitchOnAStagedWindowReplacesTheHarnessOnStage(t *testing.T) {
	e := newHomeEnv(t)
	cs := filepath.Join(e.dir, "cs")
	if err := os.WriteFile(cs, []byte("#!/bin/sh\necho thread-from-cs\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("YAMNAYA_CS_BIN", cs)
	e.project("beta")
	if err := e.a.ensureHome(e.dir); err != nil {
		t.Fatal(err)
	}
	mgr, _ := e.a.store.OpenWindow("beta", "manager")
	if err := e.a.stage(mgr.ID, false); err != nil {
		t.Fatal(err)
	}
	staged, _ := e.staged()
	e.ym("-p", "beta", "switch", "manager", "--to", "codex", "--min-idle", "0s")

	if got := e.running(mgr.SessionID); len(got) != 0 {
		t.Fatalf("old claude session still runs in %v", got)
	}
	if got := e.running("thread-from-cs"); len(got) != 1 || got[0] != staged.Pane {
		t.Fatalf("new codex session runs in %v, want only the staged pane %s", got, staged.Pane)
	}
	if p, _ := e.a.tmux.Pane(mgr.TmuxTarget); p.Role != "rest" {
		t.Fatalf("origin holds %+v, want the placeholder", p)
	}
	if st, _ := e.staged(); st.Harness != "codex" || st.SessionID != "thread-from-cs" {
		t.Fatalf("stage record not updated: %+v", st)
	}
	if err := e.a.unstage(); err != nil {
		t.Fatal(err)
	}
	if got := e.spots()[staged.Pane].window; got != mgr.TmuxTarget {
		t.Fatalf("switched harness returned to %s, want %s", got, mgr.TmuxTarget)
	}
	e.assertHomeResting()
	e.ym("land", "beta")
	if got := e.running("thread-from-cs"); len(got) != 0 {
		t.Fatalf("landed project still runs in %v", got)
	}
}

func TestReviveKeepsAStagedAgentInsteadOfStartingASecond(t *testing.T) {
	e := newHomeEnv(t)
	e.project("alpha", "w1")
	if err := e.a.ensureHome(e.dir); err != nil {
		t.Fatal(err)
	}
	w1, _ := e.a.store.OpenWindow("alpha", "w1")
	if err := e.a.stage(w1.ID, false); err != nil {
		t.Fatal(err)
	}
	staged, _ := e.staged()
	e.tmux("kill-session", "-t", "ym-alpha")
	e.ym("revive", "alpha")
	if got := e.running(w1.SessionID); len(got) != 1 || got[0] != staged.Pane {
		t.Fatalf("w1's session runs in %v, want only the staged pane %s", got, staged.Pane)
	}
	if err := e.a.unstage(); err != nil {
		t.Fatal(err)
	}
	if got := e.spots()[staged.Pane].window; got != e.target("alpha", "w1") {
		t.Fatalf("staged agent returned to %s, want w1's new window %s", got, e.target("alpha", "w1"))
	}
	e.assertHomeResting()
}

func TestReviveRefusesASessionAlreadyRunningElsewhere(t *testing.T) {
	e := newHomeEnv(t)
	e.project("alpha")
	mgr, _ := e.a.store.OpenWindow("alpha", "manager")
	e.tmux("kill-session", "-t", "ym-alpha")
	e.tmux("new-session", "-d", "-s", "scratch", "sleep 600 # resumed by hand "+mgr.SessionID)
	root := NewRoot()
	root.SetOut(io.Discard)
	root.SetArgs([]string{"revive", "alpha"})
	if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "already runs") {
		t.Fatalf("err = %v", err)
	}
	if got := e.running(mgr.SessionID); len(got) != 1 {
		t.Fatalf("session runs in %v, want only the hand-started pane", got)
	}
}
