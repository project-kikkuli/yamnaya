package tui

import (
	"fmt"
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
	err      error
}

func (s *switching) SwitchProfile(ref, id string) (string, error) {
	s.switched = append(s.switched, ref+" "+id)
	if s.err != nil {
		return "", s.err
	}
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
	m.pn.cur = m.firstPanelRow(0, 1)
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
		{"rename a host", "H", box, append(append([]string{"n", "ctrl+u"}, typed("far")...), "enter"), ymCall{args: []string{"remote", "rename", box, "far"}}},
		{"give a host its default nickname back", "H", box, []string{"n", "ctrl+u", "enter"}, ymCall{args: []string{"remote", "rename", box, ""}}},
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
			matched := false
			for _, call := range *calls {
				matched = matched || reflect.DeepEqual(call, c.want)
			}
			if !matched {
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

func TestBSwitchesTheSelectedChiefOrGoalWithoutSwitchingItsReports(t *testing.T) {
	for _, role := range []string{core.RoleChief, core.RoleManager} {
		for _, live := range []bool{true, false} {
			for _, host := range []string{"", box} {
				t.Run(fmt.Sprintf("%s/live=%t/host=%s", role, live, host), func(t *testing.T) {
					m, _, sw := panelBoard(t)
					harness, login := "claude", "work"
					if host != "" {
						harness, login = "codex", "far"
					}
					mg := core.Manager{ID: "mgr1", Role: role, Name: "platform", Session: "chief-conversation", Harness: harness, Profile: "source", Host: host}
					n := core.Node{Manager: mg, Standing: core.StandDown, Children: []core.Node{{
						Manager: core.Manager{ID: "mgr2", Role: core.RoleManager, Session: "report-conversation"},
						Goal:    &core.Goal{ID: "goal2", Ask: "ship the report"}, Standing: core.StandDown,
					}}}
					if role == core.RoleManager {
						n.Goal = &core.Goal{ID: "goal1", Ask: "ship the platform"}
					}
					if live {
						n.Live = &core.Live{Session: core.Session{ID: "live-conversation", Harness: harness, Profile: mg.Profile, Status: core.Idle}, Remote: host}
					}
					m.board.Tree = []core.Node{n}
					m = press(t, to(t, m, isNode(mg.ID)), "B")
					session := mg.Session
					if live {
						session = n.Live.ID
					}
					if m.pn.target == nil || m.pn.target.session != session {
						t.Fatalf("wrong authoritative conversation: %+v", m.pn.target)
					}
					if v := ansi.Strip(m.View().Content); !strings.Contains(v, "enter switches "+ansi.Strip(m.tagged(host, mg.Label()))) {
						t.Fatalf("profile panel does not name the selected agent:\n%s", v)
					}
					m = press(t, toPanelRow(t, m, harness+"/"+login), "enter")
					ref := harness + ":source@" + session
					if host != "" {
						ref = core.RemoteKey(host, ref)
					}
					if want := []string{ref + " " + login}; !reflect.DeepEqual(sw.switched, want) {
						t.Fatalf("switched %q, want only %q", sw.switched, want)
					}
				})
			}
		}
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

func TestEFiltersPreviewsAndResumesOtherConversations(t *testing.T) {
	at := time.Date(2026, 10, 1, 9, 0, 0, 0, time.Local)
	past := []core.Past{
		{Harness: "claude", Session: "c1", Cwd: "/src/app", Last: at, Prompts: []string{"fix the login", "and its tests"}, Transcript: "/t/c1.jsonl"},
		{Harness: "codex", Session: "x1", Cwd: "/src/web", Last: at.Add(-time.Hour), Prompts: []string{"style the login page"}},
		{Harness: "claude", Session: "lead", Cwd: "/src/lead", Last: at, Prompts: []string{"login lead work"}, Transcript: "/t/lead.jsonl"},
	}
	for _, c := range []struct {
		name       string
		keys       []string
		shows      []string
		hides      []string
		relaunched []string
		message    string
	}{
		{"filter narrows and previews the first match", typed("login tests"), []string{"fix the login", "1. fix the login", "2. and its tests", "filter: login tests"},
			[]string{"style the login page", "login lead work"}, nil, ""},
		{"enter resumes the match in its directory", append(typed("tests"), "enter"), nil, nil, []string{"c1 claude c1 /src/app"}, "revived adopt-c1"},
		{"enter on a row whose transcript is gone says so", append(typed("style"), "enter"), []string{"transcript gone"}, nil, nil, "no transcript on disk"},
		{"esc clears the filter before closing", append(typed("style"), "esc"), []string{"fix the login", "style the login page", "type to filter"}, nil, nil, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, _, sw := panelBoard(t)
			m.width, m.height = 160, 60
			m.core.History = func() ([]core.Past, error) { return past, nil }
			m = press(t, m, "E")
			m = press(t, m, c.keys...)
			if m.mode != modePanel {
				t.Fatalf("left the panel for mode %v", m.mode)
			}
			v := ansi.Strip(m.View().Content)
			for _, want := range c.shows {
				if !strings.Contains(v, want) {
					t.Errorf("lacks %q:\n%s", want, v)
				}
			}
			for _, hide := range c.hides {
				if strings.Contains(v, hide) {
					t.Errorf("shows %q:\n%s", hide, v)
				}
			}
			if !reflect.DeepEqual(sw.relaunched, c.relaunched) {
				t.Errorf("relaunched %q, want %q", sw.relaunched, c.relaunched)
			}
			if !strings.Contains(m.message, c.message) {
				t.Errorf("message %q, want %q", m.message, c.message)
			}
		})
	}
}

func TestEStillClosesEUntilSomethingIsTyped(t *testing.T) {
	m, _, _ := panelBoard(t)
	if m = press(t, m, "E", "E"); m.mode != modeList {
		t.Fatalf("E E left mode %v", m.mode)
	}
	if m = press(t, m, "E", "x", "E"); m.mode != modePanel || m.pn.past.query != "xE" {
		t.Fatalf("typing then E: mode %v query %q", m.mode, m.pn.past.query)
	}
}
