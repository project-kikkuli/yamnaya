package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/mcp"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

type mcpChoice struct{ host, harness, id, root, cwd string }
type mcpPanel struct {
	choice    *mcpChoice
	inventory *mcp.Inventory
	problem   string
}
type mcpMsg struct {
	choice    mcpChoice
	inventory *mcp.Inventory
	problem   string
}

func (c mcpChoice) args(operation string) []string {
	args := []string{"mcp", operation, "--harness", c.harness, "--profile", profile.ID(c.id), "--cwd", c.cwd}
	if c.root != "" {
		args = append(args, "--root", c.root)
	}
	return args
}

func (m Model) chooseMCP(c mcpChoice, prompt bool) (tea.Model, tea.Cmd) {
	c.id = profile.ID(c.id)
	if c.root == "" {
		m.pn.kind = panelMCP
		m.pn.mcp = mcpPanel{choice: &c, problem: "native root unknown for this session; update its host and choose a configured login explicitly"}
		return m, nil
	}
	if prompt {
		if c.host == "" {
			c.cwd, _ = os.Getwd()
		} else {
			c.cwd = "/"
			for _, r := range m.board.Remotes {
				if r.Host == c.host {
					for _, p := range r.Profiles {
						if p.ID == "default" && p.Harness == c.harness {
							c.cwd = filepath.Dir(p.Root)
							break
						}
					}
				}
			}
		}
		m.askLine("working directory on "+m.hostTag(c.host)+" for "+c.harness+"/"+profile.ID(c.id), c.cwd, false, func(m Model, work string) (tea.Model, tea.Cmd) { c.cwd = work; return m.chooseMCP(c, false) })
		return m, nil
	}
	m.pn.kind, m.pn.cur = panelMCP, 0
	m.pn.mcp = mcpPanel{choice: &c}
	return m, m.loadMCP()
}

func (m Model) loadMCP() tea.Cmd {
	if m.pn.mcp.choice == nil {
		return nil
	}
	c, run := *m.pn.mcp.choice, m.pn.run
	if c.root == "" {
		return nil
	}
	blocked := m.silent(c.host)
	return func() tea.Msg {
		msg := mcpMsg{choice: c}
		if blocked != nil {
			msg.problem = "host offline or stale; MCP status unknown"
			return msg
		}
		out, err := run(c.host, "", append(c.args("list"), "--json")...)
		if err != nil {
			msg.problem = "MCP inventory unavailable; check the selected root/cwd, host connection and ym version"
			return msg
		}
		var all []mcp.Inventory
		if json.Unmarshal([]byte(out), &all) != nil || len(all) != 1 {
			msg.problem = "MCP inventory unsupported by this host; update its ym"
			return msg
		}
		msg.inventory = &all[0]
		return msg
	}
}

func (m Model) mcpRows() ([]prow, []pdo) {
	d := m.pn.mcp
	if d.choice == nil {
		rows := []prow{{text: "Choose a login on its host; connection credentials stay there."}}
		group := func(host string, ps []core.LoginProfile) {
			rows = append(rows, prow{text: pal.bold.Render(m.hostTag(host))})
			if len(ps) == 0 {
				rows = append(rows, prow{text: "   profiles unknown; no local fallback"})
			}
			for _, p := range ps {
				c := mcpChoice{host: host, harness: p.Harness, id: p.ID, root: p.Root}
				rows = append(rows, prow{text: p.Harness + "/" + p.ID + "  " + p.Root, does: []pdo{{"enter", "MCPs for this login", func(m Model) (tea.Model, tea.Cmd) { return m.chooseMCP(c, true) }}}})
			}
		}
		group("", m.board.Profiles)
		for _, r := range m.board.Remotes {
			group(r.Host, r.Profiles)
		}
		return rows, nil
	}
	c := *d.choice
	rows := []prow{{text: pal.bold.Render(m.hostTag(c.host) + "  " + c.harness + "/" + profile.ID(c.id))}, {text: "root: " + c.root + "  cwd: " + c.cwd}}
	global := []pdo{{"r", "refresh files", func(m Model) (tea.Model, tea.Cmd) { return m, m.loadMCP() }}, {"p", "choose another login", func(m Model) (tea.Model, tea.Cmd) {
		m.pn.mcp = mcpPanel{}
		m.pn.cur = m.firstPanelRow(0, 1)
		return m, nil
	}}}
	if d.problem != "" {
		rows = append(rows, prow{text: pal.red.Render(d.problem)})
		return rows, global
	}
	if d.inventory == nil {
		return append(rows, prow{text: "reading configuration without starting servers…"}), global
	}
	rows = append(rows, prow{text: "n connect Notion · a add custom server · i login in your terminal · c check · x remove"})
	if len(d.inventory.Servers) == 0 {
		rows = append(rows, prow{text: "No file-configured MCP servers for this login. n connects Notion; a adds a server."})
	}
	for _, server := range d.inventory.Servers {
		s := server
		state := "effective file definition"
		if !s.Effective {
			state = "shadowed or inactive"
		}
		text := fmt.Sprintf("%s · %s · %s · %s\n      %s · context %s\n      config %s · approval %s · auth %s · health %s", s.Name, s.Transport, s.Scope, state, s.Source, s.Context, s.Configuration, s.Approval, s.Auth, s.Health)
		if !s.CheckedAt.IsZero() {
			text += " (last checked " + s.CheckedAt.Local().Format("Jan 2 15:04") + ")"
		}
		key := "panel:mcp:" + qual(c.host, c.harness+"/"+c.id+":"+s.Scope+":"+s.Source+":"+s.Name)
		operation := func(op string) []string {
			return append(c.args(op), s.Name, "--scope", s.Scope, "--source", s.Source, "--revision", s.Fingerprint)
		}
		var does []pdo
		if s.Effective {
			does = append(does, pdo{"i", "log in", func(m Model) (tea.Model, tea.Cmd) {
				args := operation("login")
				if c.host != "" {
					args = append(args, "--no-browser")
				}
				return m.handOver(c.host, "back from MCP login; c checks access", args...)
			}}, pdo{"l", "log out", func(m Model) (tea.Model, tea.Cmd) {
				return m.handOver(c.host, "back from MCP logout", operation("logout")...)
			}}, pdo{"c", "check explicitly", func(m Model) (tea.Model, tea.Cmd) { return m, m.runMCP(key, "checking MCP", c, operation("check")...) }})
		}
		if s.Mutable {
			does = append(does, pdo{"x", "remove this scope", func(m Model) (tea.Model, tea.Cmd) {
				m.confirm("remove "+s.Name+" from "+s.Scope+" at "+s.Source+" on "+m.hostTag(c.host)+"?", m.runMCP(key, "removing MCP", c, operation("remove")...))
				return m, nil
			}})
		}
		rows = append(rows, prow{text: text, key: key, does: does})
	}
	for _, note := range d.inventory.Notes {
		rows = append(rows, prow{text: pal.dim.Render(note)})
	}
	rows = append(rows, prow{text: "Existing sessions may need native reconnect or resume after a connection change."})
	global = append(global, pdo{"n", "connect Notion", func(m Model) (tea.Model, tea.Cmd) {
		return m.mcpScope(c, func(m Model, scope string) (tea.Model, tea.Cmd) {
			return m.confirmMCPAdd(c, "notion", scope, append(c.args("add"), "notion", "--notion", "--scope", scope))
		})
	}}, pdo{"a", "add custom server", func(m Model) (tea.Model, tea.Cmd) { return m.addMCP(c) }}, pdo{"o", "native /mcp and advanced sources", func(m Model) (tea.Model, tea.Cmd) {
		return m.handOver(c.host, "back from native MCP management", c.args("manage")...)
	}})
	return rows, global
}

func (m *Model) runMCP(key, doing string, c mcpChoice, args ...string) tea.Cmd {
	if err := m.silent(c.host); err != nil {
		return func() tea.Msg { return actMsg{key: key, err: fmt.Errorf("host unavailable; no MCP change was queued")} }
	}
	return m.runYM(key, doing, "MCP", c.host, "", args...)
}

func (m Model) addMCP(c mcpChoice) (tea.Model, tea.Cmd) {
	return m.mcpScope(c, func(m Model, scope string) (tea.Model, tea.Cmd) { return m.addMCPInScope(c, scope) })
}

func (m Model) mcpScope(c mcpChoice, then func(Model, string) (tea.Model, tea.Cmd)) (tea.Model, tea.Cmd) {
	if c.harness == "codex" {
		return then(m, "user")
	}
	m.askLine("scope: user (this login across repos), project (.mcp.json), local (this cwd)", "user", false, func(m Model, scope string) (tea.Model, tea.Cmd) {
		if scope != "user" && scope != "project" && scope != "local" {
			m.message = "error: choose user, project or local scope"
			return m, nil
		}
		return then(m, scope)
	})
	return m, nil
}

func (m Model) confirmMCPAdd(c mcpChoice, name, scope string, args []string) (tea.Model, tea.Cmd) {
	m.confirm("add "+name+" · "+m.hostTag(c.host)+" · "+c.harness+"/"+c.id+" · root "+c.root+" · cwd "+c.cwd+" · scope "+scope+"?", m.runMCP("panel:mcp:add", "configuring server; i logs in, c checks", c, args...))
	return m, nil
}

func (m Model) addMCPInScope(c mcpChoice, scope string) (tea.Model, tea.Cmd) {
	m.askLine("server name", "", false, func(m Model, name string) (tea.Model, tea.Cmd) {
		m.askLine("transport: http, stdio"+map[bool]string{true: ", sse"}[c.harness == "claude"], "http", false, func(m Model, transport string) (tea.Model, tea.Cmd) {
			if transport == "stdio" {
				m.askLine("executable on the selected host", "", false, func(m Model, exe string) (tea.Model, tea.Cmd) {
					m.askLine("arguments as a JSON array; use environment variables for secrets", "[]", false, func(m Model, line string) (tea.Model, tea.Cmd) {
						var argv []string
						if json.Unmarshal([]byte(line), &argv) != nil {
							m.message = "error: arguments must be a JSON array of strings"
							return m, nil
						}
						args := append(c.args("add"), name, "--scope", scope, "--transport", "stdio", "--", exe)
						args = append(args, argv...)
						return m.confirmMCPAdd(c, name, scope, args)
					})
					return m, nil
				})
				return m, nil
			}
			if transport != "http" && !(transport == "sse" && c.harness == "claude") {
				m.message = "error: transport unsupported by this native MCP add command"
				return m, nil
			}
			m.askLine("HTTP endpoint; no credentials or query parameters", "https://", false, func(m Model, url string) (tea.Model, tea.Cmd) {
				return m.confirmMCPAdd(c, name, scope, append(c.args("add"), name, "--scope", scope, "--transport", transport, "--url", url))
			})
			return m, nil
		})
		return m, nil
	})
	return m, nil
}
