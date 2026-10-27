package tui

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// ymCall is one ym command a panel ran, on host with stdin, or handed this terminal to when tty is set.
type ymCall struct {
	host, stdin string
	args        []string
	tty         bool
}

type switching struct {
	*launches
	switched []string
}

func (s *switching) SwitchProfile(ref, id string) (string, error) {
	s.switched = append(s.switched, ref+" "+id)
	return "switched", nil
}

// panelBoard is the fixture board with a profile and a remote host, its ym commands recorded instead of run.
func panelBoard(t *testing.T) (Model, *[]ymCall, *switching) {
	t.Helper()
	m, c, _ := setup(t)
	sw := &switching{launches: &launches{}}
	c.Launches = sw
	c.ProfileList = func() []core.LoginProfile {
		return []core.LoginProfile{{Harness: "claude", ID: "default", Label: "default"}, {Harness: "claude", ID: "work", Label: "Work", Account: "me@work", Root: "/x/.claude-work"}}
	}
	now := time.Now()
	c.Meter = meter{core.Fleet{Ready: true, At: now, Limits: []core.Limit{{Harness: "claude", Profile: "work", Account: "me@work", Name: "5h", Used: 30, Window: 5 * time.Hour, ResetsAt: now.Add(time.Hour), Seen: now}}}}
	m, r := withBox(t, m, c)
	r.boards[0].Profiles = []core.LoginProfile{{Harness: "codex", ID: "default", Label: "default", Root: "/home/me/.codex"}, {Harness: "codex", ID: "far", Label: "far"}}
	m = refresh(t, m)
	calls := &[]ymCall{}
	m.pn.run = func(host, stdin string, args ...string) (string, error) {
		if len(args) > 0 && (args[0] == "docs" && len(args) == 1 || args[0] == "service" && args[1] == "status") {
			return `{"running":true}`, nil
		}
		*calls = append(*calls, ymCall{host: host, stdin: stdin, args: args})
		return "ok", nil
	}
	m.pn.tty = func(host string, args ...string) *exec.Cmd {
		*calls = append(*calls, ymCall{host: host, args: args, tty: true})
		return exec.Command("true")
	}
	return m, calls, sw
}

// toPanelRow moves the panel cursor to the row whose text holds sub.
func toPanelRow(t *testing.T, m Model, sub string) Model {
	t.Helper()
	for range 40 {
		rows, _ := m.panelRows()
		if m.pn.cur < len(rows) && strings.Contains(ansi.Strip(rows[m.pn.cur].text), sub) {
			return m
		}
		m = press(t, m, "down")
	}
	t.Fatalf("no panel row %q on:\n%s", sub, ansi.Strip(m.View().Content))
	return m
}

func typed(text string) []string { return strings.Split(text, "") }

func TestPanelActionsRunTheCommandTheCLIRuns(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, c := range []struct {
		name  string
		panel string
		row   string
		keys  []string
		want  ymCall
	}{
		{"log in here", "B", "claude/work", []string{"i"}, ymCall{args: []string{"profiles", "login", "claude", "work"}, tty: true}},
		{"log in on a host by device auth", "B", "codex/far", []string{"i"}, ymCall{host: box, args: []string{"profiles", "login", "codex", "far", "--device-auth"}, tty: true}},
		{"make a login the launch default", "B", "claude/work", []string{"d"}, ymCall{args: []string{"profiles", "default", "claude", "work"}}},
		{"remove a login", "B", "claude/work", []string{"x", "y"}, ymCall{args: []string{"profiles", "remove", "claude", "work"}}},
		{"remove a login on a host", "B", "codex/far", []string{"x", "y"}, ymCall{host: box, args: []string{"profiles", "remove", "codex", "far"}}},
		{"add a login with the default root", "B", "", append(append([]string{"a"}, typed("Work Two")...), "enter", "enter"),
			ymCall{args: []string{"profiles", "add", "claude", "work-two", "--label", "Work Two", "--root", filepath.Join(home, ".claude-work-two")}}},
		{"add a codex login on a host", "B", "", append(append([]string{"a", "tab", "ctrl+t"}, typed("far2")...), "enter", "enter"),
			ymCall{host: box, args: []string{"profiles", "add", "codex", "far2", "--label", "far2", "--root", "/home/me/.codex-far2"}}},
		{"add a login with an edited root", "B", "", append(append(append([]string{"a"}, typed("p")...), "enter", "ctrl+u"), append(typed("/r/p"), "enter")...),
			ymCall{args: []string{"profiles", "add", "claude", "p", "--label", "p", "--root", "/r/p"}}},
		{"install ym on a host", "H", box, []string{"i"}, ymCall{args: []string{"remote", "install", box}}},
		{"forget a host", "H", box, []string{"x", "y"}, ymCall{args: []string{"remote", "forget", box}}},
		{"open a host's board", "H", box, []string{"o"}, ymCall{host: box, tty: true}},
		{"add a host", "H", "", append(append([]string{"a"}, typed("newbox")...), "enter"), ymCall{args: []string{"remote", "install", "newbox"}}},
		{"drop a refused relay", "H", "ym kill", []string{"x", "y"}, ymCall{args: []string{"relay", "drop", "7"}}},
		{"restart ym serve", "Y", "running", []string{"enter", "y"}, ymCall{args: []string{"service", "restart"}}},
		{"keep ym serve", "Y", "not kept", []string{"enter"}, ymCall{args: []string{"service", "keep"}}},
		{"unkeep ym serve", "Y", "kept running", []string{"enter"}, ymCall{args: []string{"service", "unkeep"}}},
		{"connect Notion with the token on stdin", "Y", `{"running"`, append(append(append([]string{"enter"}, typed("me@x.io")...), "enter"), append(typed("secret_tok"), "enter")...),
			ymCall{stdin: "secret_tok\n", args: []string{"docs", "connect", "me@x.io"}}},
		{"set the context cap", "Y", "tokens", append(append([]string{"enter", "ctrl+u"}, typed("200k")...), "enter"), ymCall{args: []string{"goal", "context-cap", "200k"}}},
		{"clear a repository for merges", "Y", "add a repository", append(append([]string{"enter"}, typed("yamnaya")...), "enter"), ymCall{args: []string{"goal", "authority", "yamnaya", "--merge"}}},
		{"update ym", "Y", "update ym", []string{"enter", "y"}, ymCall{args: []string{"update"}}},
		{"revive an exited launch", "E", "fix-login", []string{"enter"}, ymCall{args: []string{"revive", "fix-login"}}},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, calls, _ := panelBoard(t)
			m = press(t, m, c.panel)
			if m.mode != modePanel {
				t.Fatalf("%s opened mode %v", c.panel, m.mode)
			}
			if c.name == "unkeep ym serve" {
				m.pn.settings.service["kept"] = "/x/io.yamnaya.ym-serve.plist"
			}
			m.pn.relays = []core.Relay{{Seq: 7, Dest: box, Kind: core.RelayYM, Body: `{"args":["kill","far-lead"]}`, Created: time.Now(), Error: "locked"}}
			if c.row != "" {
				m = toPanelRow(t, m, c.row)
			}
			m = press(t, m, c.keys...)
			if len(*calls) == 0 || !reflect.DeepEqual((*calls)[len(*calls)-1], c.want) {
				t.Fatalf("ran %+v, want %+v; message %q\n%s", *calls, c.want, m.message, ansi.Strip(m.View().Content))
			}
		})
	}
}

func TestNotionTokenNeverShowsWhileTyped(t *testing.T) {
	m, _, _ := panelBoard(t)
	m = press(t, m, "Y")
	m = toPanelRow(t, m, `{"running"`)
	m = press(t, m, append(append(append([]string{"enter"}, typed("me@x.io")...), "enter"), typed("secret_tok")...)...)
	if v := ansi.Strip(m.View().Content); strings.Contains(v, "secret_tok") || !strings.Contains(v, "••••••••••") {
		t.Fatalf("token shown while typed:\n%s", v)
	}
	if m = press(t, m, "enter"); strings.Contains(ansi.Strip(m.View().Content), "secret_tok") || len(m.input) != 0 {
		t.Fatalf("token kept after enter: %q", string(m.input))
	}
}

func TestBSwitchesTheSessionToTheChosenLogin(t *testing.T) {
	for _, c := range []struct {
		name, row, want string
	}{
		{"named login", "claude/work", "lead work"},
		{"auto", "auto:", "lead auto"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, sw := panelBoard(t)
			m = press(t, to(t, m, mission("lead")), "B")
			if m.mode != modePanel || m.pn.kind != panelProfiles {
				t.Fatalf("B opened mode %v panel %v", m.mode, m.pn.kind)
			}
			m = press(t, toPanelRow(t, m, c.row), "enter")
			if len(sw.switched) != 1 || sw.switched[0] != c.want {
				t.Fatalf("switched %q, want %q", sw.switched, c.want)
			}
		})
	}
}

func TestProfilesPanelListsEveryHostsLoginsWithTheirWindows(t *testing.T) {
	m, _, _ := panelBoard(t)
	m.width = 200
	m = press(t, m, "B")
	v := ansi.Strip(m.View().Content)
	for _, want := range []string{"claude/work", "me@work", "5h 30%", "/x/.claude-work", box, "codex/far"} {
		if !strings.Contains(v, want) {
			t.Errorf("profiles panel lacks %q:\n%s", want, v)
		}
	}
	if m = press(t, m, "B"); m.mode != modeList {
		t.Fatalf("B again left mode %v", m.mode)
	}
}
