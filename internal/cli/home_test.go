package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

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
		{"agent exited on stage", func(e *testEnv, agent string) {
			e.tmux("kill-pane", "-t", agent)
		}, func(e *testEnv, agent string) {
			if strings.Contains(e.tmux("list-windows", "-a", "-F", "#{window_id}"), e.target("alpha", "w1")) {
				e.t.Fatal("origin window still holds the placeholder")
			}
		}},
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
	press(m, "n", "gamma", "enter")
	m.prompt.fields[1].value = e.dir
	press(m, "enter", "enter", "plan,build", "enter", "go", "enter")
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
