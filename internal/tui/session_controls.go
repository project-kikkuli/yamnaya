package tui

import (
	"errors"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// killTargets are the sessions K on r ends, live or exited, and how the confirmation names them.
func (m Model) killTargets(r row) ([]core.Live, string) {
	if s := m.killable(r); s != nil {
		return []core.Live{*s}, s.Label()
	}
	dormant := func(d core.Dormant) core.Live {
		return core.Live{Session: core.Session{ID: d.ID(), Harness: d.Harness, Title: d.Title}}
	}
	if r.dormant != nil {
		return []core.Live{dormant(*r.dormant)}, r.dormant.Key
	}
	if r.need != nil || r.item != nil || r.stale || r.member != nil {
		return nil, ""
	}
	if p := m.projectOf(r); p != nil && r.session == nil && (len(p.Sessions) > 0 || len(p.Dormant) > 0) {
		var out []core.Live
		out = append(out, p.Sessions...)
		for _, d := range p.Dormant {
			out = append(out, dormant(d))
		}
		return out, p.Name
	}
	return nil, ""
}

func (m Model) sessionHints() string {
	switch m.mode {
	case modeList, modeRepo, modeDetail, modeSession, modeAgent:
	default:
		return ""
	}
	r, ok := m.selected()
	if !ok && m.mode != modeSession && m.mode != modeAgent {
		return ""
	}
	var hints []string
	if ss, _ := m.killTargets(r); len(ss) > 0 && !m.dying(ss) {
		hints = append(hints, "K kill")
	}
	if r.dormant != nil {
		hints = append(hints, "B subscription")
	}
	if s := m.sessionOf(r); s != nil {
		if s.ID != "" && m.core.Launches != nil {
			hints = append(hints, "s send")
		}
		hints = append(hints, "T switch", "B subscription")
	}
	return strings.Join(hints, hintSep)
}

// dying reports whether a kill already tears down every session in ss.
func (m Model) dying(ss []core.Live) bool {
	for _, s := range ss {
		if !m.killing[sessionKey(s)] {
			return false
		}
	}
	return len(ss) > 0
}

// rowDying reports whether a kill started from the board is tearing r down.
func (m Model) rowDying(r row) bool {
	if r.node != nil && r.session == nil {
		return m.killing["node:"+r.node.Manager.ID]
	}
	ss, _ := m.killTargets(r)
	return m.dying(ss)
}

// weighedMsg carries what a kill would end, counted off the UI thread, to ask before it runs.
type weighedMsg struct {
	ask  string
	keys []string
	run  func() (string, error)
	err  error
}

// killedMsg reports a kill's teardown, run off the UI thread, ending the rows it marked dying.
type killedMsg struct {
	keys    []string
	message string
	err     error
}

// weighKill counts what killing ss would end, then asks; yes tears them down off the UI thread, marking their rows dying.
func (m Model) weighKill(ss []core.Live, label string) tea.Cmd {
	c := m.core
	return func() tea.Msg {
		keys := make([]string, len(ss))
		for i, s := range ss {
			keys[i] = sessionKey(s)
		}
		return weighedMsg{ask: fmt.Sprintf("kill %s: %s?", label, c.Weigh(ss)), keys: keys, run: func() (string, error) {
			var said []string
			var errs []error
			for _, s := range ss {
				msg, err := c.Kill(s)
				if err != nil {
					errs = append(errs, err)
					continue
				}
				said = append(said, msg)
			}
			return strings.Join(said, "; "), errors.Join(errs...)
		}}
	}
}

// weighGoal counts what killing the goal or chief n would end, then asks as weighKill does.
func (m Model) weighGoal(n core.Node) tea.Cmd {
	c, b := m.core, m.board
	id, label, host := n.Manager.ID, n.Manager.Label(), n.Manager.Host
	if n.Goal != nil {
		label = n.Goal.ID + " (" + oneLine(n.Goal.Ask) + ")"
	}
	return func() tea.Msg {
		keys := []string{"node:" + id}
		run := func() (string, error) { return c.KillGoal(b, id) }
		if n.Goal == nil {
			return weighedMsg{ask: "retire chief " + label + "? its session ends; its goals and chiefs keep running and report one level up", keys: keys, run: run}
		}
		if host != "" {
			ss := []core.Live{}
			if n.Live != nil {
				ss = append(ss, *n.Live)
			}
			ss = append(ss, n.Agents...)
			return weighedMsg{ask: fmt.Sprintf("kill goal %s on %s with its manager, agents and verifier: %s?", label, host, c.Weigh(ss)), keys: keys, run: run}
		}
		_, ss, err := c.KillTargets(b, id)
		if err != nil {
			return weighedMsg{err: err}
		}
		for _, s := range ss {
			keys = append(keys, s.Key())
		}
		return weighedMsg{ask: fmt.Sprintf("kill goal %s: %s?", label, c.Weigh(ss)), keys: keys, run: run}
	}
}
