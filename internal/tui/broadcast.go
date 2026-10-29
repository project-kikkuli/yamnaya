package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

// broadcastKey opens a message to every agent under the cursor's level.
const broadcastKey = "!"

// broadcastTargets is every live agent under the level the cursor is on, and that level's name: a goal or chief
// and everything below it, a session, a mission, a repository, the view it is drilled into, else every agent.
func (m Model) broadcastTargets() ([]core.Live, string) {
	r, ok := m.selected()
	switch {
	case ok && r.node != nil:
		return m.nodeAgents(*r.node), nodeScope(*r.node)
	case ok && r.session != nil && r.need == nil:
		return []core.Live{*r.session}, r.session.Label()
	case ok && r.project != nil && r.need == nil && r.item == nil && r.dormant == nil:
		return r.project.Sessions, "mission " + r.project.Name
	case ok && r.repo != nil:
		return m.repoAgents(r.repo.Key), "repository " + r.repo.Name
	}
	switch m.mode {
	case modeGoal, modeChief:
		id := m.goal
		if m.mode == modeChief {
			id = m.chief
		}
		if n, ok := m.board.Node(id); ok {
			return m.nodeAgents(n), nodeScope(n)
		}
	case modeRepo:
		if rp, ok := m.board.Repo(m.repo); ok {
			return m.repoAgents(rp.Key), "repository " + rp.Name
		}
	case modeDetail:
		if p, ok := m.board.Project(m.detail); ok {
			return p.Sessions, "mission " + p.Name
		}
	case modeSession, modeAgent:
		if s, ok := m.session(); ok {
			return []core.Live{*s}, s.Label()
		}
	}
	return m.board.Sessions, "every agent"
}

func nodeScope(n core.Node) string {
	if len(n.Children) > 0 {
		return n.Manager.Label() + " and everyone below"
	}
	return n.Manager.Label()
}

// nodeAgents is the node's manager session and agents, and those of every node below it.
func (m Model) nodeAgents(n core.Node) []core.Live {
	var out []core.Live
	if n.Live != nil {
		out = append(out, *n.Live)
	}
	out = append(out, n.Agents...)
	for _, c := range n.Children {
		out = append(out, m.nodeAgents(c)...)
	}
	return out
}

// repoAgents is every session of the repository's missions and of the goals working in it.
func (m Model) repoAgents(key string) []core.Live {
	var out []core.Live
	if rp, ok := m.board.Repo(key); ok {
		for _, p := range rp.Missions {
			out = append(out, p.Sessions...)
		}
	}
	var walk func([]core.Node)
	walk = func(ns []core.Node) {
		for _, n := range ns {
			if n.Goal != nil && n.Repo == key {
				out = append(out, m.nodeAgents(n)...)
			}
			walk(n.Children)
		}
	}
	walk(m.board.Tree)
	return out
}

// recipients keeps the sessions a message can reach, once each.
func recipients(ss []core.Live) []string {
	seen := map[string]bool{}
	var keys []string
	for _, s := range ss {
		if s.ID == "" || seen[s.Key()] {
			continue
		}
		seen[s.Key()] = true
		keys = append(keys, s.Key())
	}
	return keys
}

// startBroadcast resolves the cursor's scope and asks for the message, naming the scope and how many it reaches.
func (m Model) startBroadcast() (tea.Model, tea.Cmd) {
	if m.core.Launches == nil {
		m.message = "broadcast needs the launcher, which is unavailable"
		return m, nil
	}
	ss, scope := m.broadcastTargets()
	keys := recipients(ss)
	if len(keys) == 0 {
		m.message = "no live agent under " + scope
		return m, nil
	}
	m.startInput(inBroadcast, "", "", "", false)
	m.bcast = keys
	m.inLabel = fmt.Sprintf("%s (%s)", scope, plural(len(keys), "agent"))
	return m, nil
}

// broadcast sends text to every recipient still on the board, the way s sends to one.
func (m Model) broadcast(text string) (tea.Model, tea.Cmd) {
	if text == "" {
		return m, nil
	}
	var ss []core.Live
	for _, k := range m.bcast {
		if s, ok := m.board.Session(k); ok {
			ss = append(ss, s)
		}
	}
	gone := len(m.bcast) - len(ss)
	m.bcast = nil
	l := m.core.Launches
	return m, m.do(func() (string, error) {
		var failed []string
		for _, s := range ss {
			if _, err := l.Send(s, text); err != nil {
				failed = append(failed, s.Label()+": "+err.Error())
			}
		}
		msg := fmt.Sprintf("broadcast to %s", plural(len(ss)-len(failed), "agent"))
		if gone > 0 {
			msg += fmt.Sprintf("; %d gone", gone)
		}
		if len(failed) > 0 {
			return msg, fmt.Errorf("%d failed: %s", len(failed), strings.Join(failed, "; "))
		}
		return msg, nil
	})
}
