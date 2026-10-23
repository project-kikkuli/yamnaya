package tui

import (
	"fmt"
	"strings"
)

func (m Model) landingTargets(r row) ([]string, string) {
	if r.dormant != nil {
		return []string{r.dormant.Key}, r.dormant.Key
	}
	if s := m.sessionOf(r); s != nil && s.ID != "" {
		if m.hasLaunch(s.ID) {
			return []string{s.ID}, s.Label()
		}
		return nil, ""
	}
	if p := m.projectOf(r); p != nil && len(p.Sessions) == 1 && p.Sessions[0].ID != "" {
		if m.hasLaunch(p.Sessions[0].ID) {
			return []string{p.Sessions[0].ID}, p.Sessions[0].Label()
		}
		return nil, ""
	}
	if p := m.projectOf(r); p != nil && len(p.Dormant) > 0 {
		var refs []string
		for _, d := range p.Dormant {
			refs = append(refs, d.Key)
		}
		return refs, fmt.Sprintf("%s's %d exited sessions", p.Name, len(refs))
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
	if m.killable(r) != nil {
		hints = append(hints, "K kill")
	} else if ids, _ := m.stoppedTargets(r); len(ids) > 0 {
		hints = append(hints, "K archive")
	}
	if r.dormant != nil {
		hints = append(hints, "B subscription")
	}
	refs, _ := m.landingTargets(r)
	if len(refs) > 0 && m.core.Launches != nil {
		hints = append(hints, "L land", "F forget")
	}
	if s := m.sessionOf(r); s != nil {
		if s.ID != "" && m.core.Launches != nil {
			hints = append(hints, "s send")
		}
		hints = append(hints, "T switch", "B subscription")
	}
	return strings.Join(hints, "   ")
}

func (m Model) hasLaunch(session string) bool {
	for _, l := range m.board.Launched {
		if l.Session == session && l.Landed.IsZero() {
			return true
		}
	}
	return false
}

func (m Model) stoppedTargets(r row) ([]string, string) {
	if r.dormant != nil {
		return []string{r.dormant.ID()}, r.dormant.Title
	}
	if p := m.projectOf(r); p != nil && len(p.Sessions) == 0 {
		var ids []string
		for _, d := range p.Dormant {
			ids = append(ids, d.ID())
		}
		return ids, p.Name
	}
	return nil, ""
}
