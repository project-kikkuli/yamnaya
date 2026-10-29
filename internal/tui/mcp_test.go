package tui

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/mcp"
)

func mcpPanelFixture(t *testing.T, host, harness, id string) (Model, *[]ymCall, mcpChoice) {
	t.Helper()
	m, calls, _ := panelBoard(t)
	choice := mcpChoice{host: host, harness: harness, id: id, root: "/native/" + id, cwd: "/repo/project"}
	m.mode = modePanel
	m.pn.kind = panelMCP
	m.pn.mcp = mcpPanel{choice: &choice, inventory: &mcp.Inventory{Target: mcp.Target{Host: host, Harness: harness, Profile: id, Root: choice.root, Cwd: choice.cwd}, Servers: []mcp.Server{}}}
	m.pn.run = func(host, stdin string, args ...string) (string, error) {
		*calls = append(*calls, ymCall{host: host, stdin: stdin, args: args})
		if len(args) > 1 && args[1] == "list" {
			b, _ := json.Marshal([]mcp.Inventory{*m.pn.mcp.inventory})
			return string(b), nil
		}
		return "configured", nil
	}
	return m, calls, choice
}

func TestMCPAddFlowsChooseScopeAndConfirmExactDestination(t *testing.T) {
	for _, host := range []string{"", box} {
		for _, scope := range []string{"user", "local", "project"} {
			for _, kind := range []string{"notion", "http", "stdio"} {
				t.Run(fmt.Sprint(host, scope, kind), func(t *testing.T) {
					m, calls, c := mcpPanelFixture(t, host, "claude", "work")
					key := "a"
					if kind == "notion" {
						key = "n"
					}
					m = press(t, m, key, "ctrl+u", scope, "enter")
					name := "notion"
					want := append(c.args("add"), "notion", "--notion", "--scope", scope)
					if kind != "notion" {
						name = "custom"
						m = press(t, m, "custom", "enter", "ctrl+u", kind, "enter")
						want = append(c.args("add"), name, "--scope", scope, "--transport", kind)
						if kind == "http" {
							m = press(t, m, "ctrl+u", "https://server.invalid/mcp", "enter")
							want = append(want, "--url", "https://server.invalid/mcp")
						} else {
							m = press(t, m, "/host/server", "enter", "ctrl+u", `["--flag","literal $HOME"]`, "enter")
							want = append(want, "--", "/host/server", "--flag", "literal $HOME")
						}
					}
					if m.mode != modeConfirm {
						t.Fatalf("add skipped confirmation: %s", m.message)
					}
					for _, piece := range []string{name, c.root, c.cwd, scope, "claude/work"} {
						if !strings.Contains(m.View().Content, piece) {
							t.Fatalf("destination missing %s", piece)
						}
					}
					if len(*calls) != 0 {
						t.Fatal("add ran before confirmation")
					}
					m = press(t, m, "y")
					if len(*calls) == 0 || !reflect.DeepEqual((*calls)[0], ymCall{host: host, args: want}) {
						t.Fatalf("calls=%+v want=%+v", *calls, want)
					}
				})
			}
		}
	}
}

func TestMCPAuthHandsOwnerTerminalExactHostRootAndScope(t *testing.T) {
	for _, host := range []string{"", box} {
		for _, h := range []string{"claude", "codex"} {
			for _, id := range []string{"default", "work"} {
				m, calls, c := mcpPanelFixture(t, host, h, id)
				s := mcp.Server{Name: "notion", Scope: "user", Source: "/native/source", Effective: true, Mutable: true, Fingerprint: "revision"}
				m.pn.mcp.inventory.Servers = []mcp.Server{s}
				loaded, _ := m.Update(mcpMsg{choice: c, inventory: m.pn.mcp.inventory})
				m = loaded.(Model)
				m = press(t, m, "i")
				want := append(c.args("login"), s.Name, "--scope", s.Scope, "--source", s.Source, "--revision", s.Fingerprint)
				if host != "" {
					want = append(want, "--no-browser")
				}
				if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], ymCall{host: host, args: want, tty: true}) {
					t.Fatalf("auth handover=%+v want=%v", *calls, want)
				}
			}
		}
	}
}

func TestMCPMissingOrReboundRootCannotReachNativeActions(t *testing.T) {
	for _, scenario := range []string{"missing", "rebound", "unsupported"} {
		m, calls, c := mcpPanelFixture(t, box, "claude", "work")
		if scenario == "missing" {
			c.root = ""
			next, cmd := m.chooseMCP(c, false)
			m = next.(Model)
			if cmd != nil {
				t.Fatal("missing root started inventory")
			}
		} else {
			msg := mcpMsg{choice: c, problem: "unsupported"}
			if scenario == "rebound" {
				msg.problem = ""
				msg.inventory = &mcp.Inventory{Target: mcp.Target{Harness: c.harness, Profile: c.id, Root: "/different/root"}}
			}
			next, _ := m.Update(msg)
			m = next.(Model)
		}
		m = press(t, m, "n", "a", "i", "c", "x")
		if len(*calls) != 0 || m.pn.mcp.problem == "" {
			t.Fatalf("unsafe/unsupported target remained actionable: %+v", *calls)
		}
	}
}

func TestMCPPanelIgnoresLateInventoryForAnotherTarget(t *testing.T) {
	m, _, c := mcpPanelFixture(t, "", "claude", "work")
	other := c
	other.root = "/other"
	next, _ := m.Update(mcpMsg{choice: other, problem: "late failure"})
	m = next.(Model)
	if m.pn.mcp.problem != "" {
		t.Fatal("late result replaced selected target")
	}
}

func TestMCPPublicKeyInspectsSelectedSessionsExactTarget(t *testing.T) {
	for _, host := range []string{"", box} {
		for _, h := range []string{"claude", "codex"} {
			for _, id := range []string{"default", "work"} {
				m, calls, c := mcpPanelFixture(t, host, h, id)
				session := core.Live{Session: core.Session{ID: "conversation", Harness: h, Profile: id, ConfigRoot: c.root, Cwd: c.cwd}, Remote: host}
				m.board = core.Board{Sessions: []core.Live{session}, Profiles: m.board.Profiles, Remotes: m.board.Remotes}
				m.mode, m.sess, m.detail = modeSession, "conversation", ""
				m = press(t, m, "F")
				if m.mode != modePanel || m.pn.kind != panelMCP || m.pn.mcp.choice == nil || *m.pn.mcp.choice != c {
					t.Fatalf("public key lost session target: %+v", m.pn.mcp.choice)
				}
				want := append(c.args("list"), "--json")
				if len(*calls) != 1 || !reflect.DeepEqual((*calls)[0], ymCall{host: host, args: want}) {
					t.Fatalf("inventory=%+v want=%v", *calls, want)
				}
			}
		}
	}
}

var _ tea.Model = Model{}
