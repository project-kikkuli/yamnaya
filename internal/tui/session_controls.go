package tui

import (
	"fmt"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func (m Model) landingTargets(r row) ([]string, string) {
	if r.dormant != nil {
		return []string{r.dormant.Key}, r.dormant.Key
	}
	if s := m.sessionOf(r); s != nil && s.ID != "" {
		return []string{s.ID}, s.Label()
	}
	if p := m.projectOf(r); p != nil && len(p.Sessions) == 1 && p.Sessions[0].ID != "" {
		return []string{p.Sessions[0].ID}, p.Sessions[0].Label()
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
	if r.review != nil || r.attention != "" {
		return ""
	}
	var hints []string
	if m.killable(r) != nil {
		hints = append(hints, "K kill")
	}
	refs, _ := m.landingTargets(r)
	if len(refs) > 0 {
		if m.core.Launches != nil {
			hints = append(hints, "L land", "F forget")
		}
	} else if _, ok := m.archivableWork(r); ok {
		hints = append(hints, "L archive")
	}
	if s := m.sessionOf(r); s != nil {
		if s.ID != "" && m.core.Launches != nil {
			hints = append(hints, "s send")
		}
		hints = append(hints, "T switch")
	}
	return strings.Join(hints, "   ")
}

func (m Model) archivableWork(r row) (work.Scope, bool) {
	w, ok := m.selectedWork()
	if p := m.projectOf(r); p != nil && len(p.Sessions) > 0 {
		return w, false
	}
	return w, ok && w.InFlight()
}
