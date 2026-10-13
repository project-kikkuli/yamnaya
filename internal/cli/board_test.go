package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/project-kikkuli/yamnaya/internal/store"
)

func newTestBoard(e *testEnv) *boardModel {
	m := newBoard(e.a)
	m.refresh()
	return m
}

// press sends each key to the board and returns the command from the last one.
func press(m *boardModel, keys ...string) tea.Cmd {
	var cmd tea.Cmd
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "up":
			msg = tea.KeyMsg{Type: tea.KeyUp}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		_, cmd = m.Update(msg)
	}
	return cmd
}

func quits(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func visible(m *boardModel) []string {
	var out []string
	for _, r := range m.rows() {
		name := r.project
		if r.kind != rowProject {
			name += "/" + r.window
		}
		out = append(out, name)
	}
	return out
}

func TestBoardExpandsSelectedAndAskingProjectsOnly(t *testing.T) {
	e := newTestEnv(t)
	e.project("alpha", "w1")
	e.project("beta", "b1")
	e.project("gamma")
	e.item("gamma", "", store.KindDecision)
	m := newTestBoard(e)

	cases := []struct {
		keys []string
		want string
	}{
		{nil, "alpha alpha/manager alpha/w1 beta gamma gamma/manager"},
		{[]string{"j", "j", "j"}, "alpha beta beta/manager beta/b1 gamma gamma/manager"},
		{[]string{"j", "j", "j"}, "alpha beta gamma gamma/manager"},
		{[]string{"k", "k"}, "alpha alpha/manager alpha/w1 beta gamma gamma/manager"},
	}
	for i, c := range cases {
		press(m, c.keys...)
		if got := strings.Join(visible(m), " "); got != c.want {
			t.Fatalf("step %d: rows %q, want %q", i, got, c.want)
		}
	}
}

func TestBoardEnterSwitchesClientAndCloses(t *testing.T) {
	cases := []struct {
		name   string
		keys   []string
		window string
	}{
		{"window row", []string{"j", "j"}, "w1"},
		{"project row jumps to manager", nil, "manager"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.project("alpha", "w1")
			e.tmux("new-session", "-d", "-s", "elsewhere", "sleep 600")
			e.attach("elsewhere")
			m := newTestBoard(e)
			press(m, c.keys...)
			if !quits(press(m, "enter")) {
				t.Fatal("enter did not close the board")
			}
			if got, want := e.clientWindow(), e.target("alpha", c.window); got != want {
				t.Fatalf("client on %s, want %s", got, want)
			}
		})
	}
}

func TestBoardItemActions(t *testing.T) {
	cases := []struct {
		name   string
		kind   string
		keys   []string
		state  string
		answer string
		reason string
		status string
	}{
		{"y accepts a default", store.KindDefault, []string{"y"}, store.ItemAnswered, "yes", "", ""},
		{"n vetoes a default", store.KindDefault, []string{"n"}, store.ItemAnswered, "no", "", ""},
		{"y leaves a decision open", store.KindDecision, []string{"y"}, store.ItemOpen, "", "", "y/n answer veto-able defaults only"},
		{"s snoozes", store.KindDecision, []string{"s"}, store.ItemSnoozed, "", "", ""},
		{"d dismisses with a reason", store.KindChore, []string{"d", "not", " ", "needed", "enter"}, store.ItemDismissed, "", "not needed", ""},
		{"d needs a reason", store.KindChore, []string{"d", "enter"}, store.ItemOpen, "", "", "dismiss needs a reason"},
		{"esc cancels a dismiss", store.KindChore, []string{"d", "x", "esc"}, store.ItemOpen, "", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.project("alpha", "w1")
			it := e.item("alpha", "w1", c.kind)
			m := newTestBoard(e)
			press(m, "o")
			press(m, c.keys...)
			got, err := e.a.store.Item("alpha", it.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State != c.state || got.Answer != c.answer || got.Reason != c.reason {
				t.Errorf("item = %s/%q/%q, want %s/%q/%q", got.State, got.Answer, got.Reason, c.state, c.answer, c.reason)
			}
			if m.status != c.status {
				t.Errorf("status = %q, want %q", m.status, c.status)
			}
			if open := len(m.items); (c.state == store.ItemOpen) != (open == 1) {
				t.Errorf("board lists %d open items after %s", open, c.state)
			}
		})
	}
}

func TestBoardItemExpandShowsDetail(t *testing.T) {
	e := newTestEnv(t)
	e.project("alpha", "w1")
	if _, err := e.a.store.AddItem(store.Item{Project: "alpha", Kind: store.KindDefault, Question: "Use sqlite?", Rec: "yes",
		Options: []store.Option{{Label: "yes", Effect: "one store"}}, Assumption: "one host", SourceWindow: "w1"}); err != nil {
		t.Fatal(err)
	}
	m := newTestBoard(e)
	m.now = func() time.Time { return time.Now().Add(90 * time.Minute) }
	press(m, "o", "enter")
	view := m.View()
	for _, want := range []string{"Use sqlite?", "rec yes", "option yes → one store", "assumes one host", "from w1", "age 1h"} {
		if !strings.Contains(view, want) {
			t.Errorf("expanded item lacks %q:\n%s", want, view)
		}
	}
	press(m, "enter")
	if strings.Contains(m.View(), "assumes one host") {
		t.Error("second enter did not collapse the item")
	}
}

func TestBoardGoAndFollowUpJumpToSource(t *testing.T) {
	cases := []struct {
		key   string
		typed bool
	}{
		{"g", false},
		{"f", true},
	}
	for _, c := range cases {
		t.Run(c.key, func(t *testing.T) {
			e := newTestEnv(t)
			e.project("alpha", "w1")
			it := e.item("alpha", "w1", store.KindDecision)
			e.attach("ym-alpha")
			m := newTestBoard(e)
			if !quits(press(m, "o", c.key)) {
				t.Fatalf("%s did not close the board", c.key)
			}
			target := e.target("alpha", "w1")
			if got := e.clientWindow(); got != target {
				t.Fatalf("client on %s, want %s", got, target)
			}
			want := "re " + it.ID + ":"
			var pane string
			for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
				if pane = e.tmux("capture-pane", "-p", "-t", target); strings.Contains(pane, want) {
					break
				}
			}
			if strings.Contains(pane, want) != c.typed {
				t.Fatalf("pane typed=%v, want %v:\n%s", strings.Contains(pane, want), c.typed, pane)
			}
			if got, _ := e.a.store.Item("alpha", it.ID); got.State != store.ItemOpen {
				t.Errorf("jumping changed item state to %s", got.State)
			}
		})
	}
}

func TestBoardStartsSideThread(t *testing.T) {
	e := newTestEnv(t)
	e.project("alpha")
	m := newTestBoard(e)
	press(m, "t", "probe", "enter")
	press(m, "esc")
	if m.prompt != nil {
		t.Fatal("esc on the harness field left the prompt open")
	}
	if _, err := e.a.store.OpenWindow("alpha", "probe"); err == nil {
		t.Fatal("cancelled prompt still spawned")
	}
	press(m, "t", "probe", "enter", "enter")
	w, err := e.a.store.OpenWindow("alpha", "probe")
	if err != nil {
		t.Fatal(err)
	}
	if w.Role != store.RoleWorker || w.Harness != "claude" || !w.Managed {
		t.Errorf("window = %+v", w)
	}
	if !strings.Contains(e.tmux("list-windows", "-t", "ym-alpha", "-F", "#{window_name}"), "probe") {
		t.Error("no tmux window for the side thread")
	}
	if m.sel.window != "probe" {
		t.Errorf("selection = %+v, want the new thread", m.sel)
	}
}

func TestBoardAdoptsUnmanagedWindow(t *testing.T) {
	e := newTestEnv(t)
	e.project("alpha")
	e.project("beta")
	e.tmux("new-session", "-d", "-s", "scratch", "-n", "my notes", "sleep 600")
	stray := e.tmux("display-message", "-p", "-t", "scratch:my notes", "#{window_id}")
	m := newTestBoard(e)
	if got := visible(m); got[len(got)-1] != "/my notes" {
		t.Fatalf("rows %v do not end with the unmanaged window", got)
	}
	press(m, "j", "j", "j", "j", "j", "j", "a")
	if m.prompt == nil || m.prompt.fields[1].value != "my-notes" {
		t.Fatalf("adopt prompt = %+v", m.prompt)
	}
	press(m, "beta", "enter", "enter")
	w, err := e.a.store.OpenWindow("beta", "my-notes")
	if err != nil {
		t.Fatal(err)
	}
	if w.TmuxTarget != stray || w.Managed || w.Harness != "claude" {
		t.Errorf("adopted window = %+v", w)
	}
	if len(m.snap.Unmanaged) != 0 {
		t.Errorf("unmanaged still lists %v", m.snap.Unmanaged)
	}
	if strings.Join(visible(m), " ") != "alpha beta beta/manager beta/my-notes" {
		t.Errorf("rows after adopt: %v", visible(m))
	}
}

func TestAdoptCommand(t *testing.T) {
	cases := []struct {
		name    string
		command string
		flags   []string
		want    string
		harness string
	}{
		{"defaults from tmux", "sleep 600", nil, "scratch-1", "claude"},
		{"flags win", "sleep 600", []string{"--name", "probe", "--harness", "codex"}, "probe", "codex"},
		{"detects codex", "codex 600", nil, "scratch-1", "codex"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.project("alpha")
			buildSleeper(t, filepath.Join(e.dir, "codex"))
			e.tmux("new-session", "-d", "-s", "scratch", "-n", "scratch.1", "-c", e.dir, "PATH="+e.dir+":$PATH "+c.command)
			time.Sleep(200 * time.Millisecond)
			e.ym(append([]string{"adopt", "scratch:scratch.1", "-p", "alpha"}, c.flags...)...)
			w, err := e.a.store.OpenWindow("alpha", c.want)
			if err != nil {
				t.Fatal(err)
			}
			if w.Harness != c.harness || w.Managed {
				t.Errorf("window = %+v", w)
			}
			if out := e.ym("ls"); !strings.Contains(out, c.want) {
				t.Errorf("ls does not show %s:\n%s", c.want, out)
			}
		})
	}
	t.Run("twice is refused", func(t *testing.T) {
		e := newTestEnv(t)
		e.project("alpha")
		root := NewRoot()
		root.SetArgs([]string{"adopt", e.target("alpha", "manager"), "-p", "alpha"})
		if err := root.Execute(); err == nil || !strings.Contains(err.Error(), "already alpha/manager") {
			t.Fatalf("err = %v", err)
		}
	})
}

// buildSleeper builds a binary that sleeps, so tmux reports path's base name as the pane command.
func buildSleeper(t *testing.T, path string) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(src, []byte("package main\n\nimport \"time\"\n\nfunc main() { time.Sleep(time.Hour) }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("go", "build", "-o", path, src).CombinedOutput(); err != nil {
		t.Fatalf("build sleeper: %v\n%s", err, out)
	}
}
