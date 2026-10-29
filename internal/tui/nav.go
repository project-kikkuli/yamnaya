package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// section is the part of the board a list row sits in: 0 needs you, 1 goals, 2 sessions.
func section(r row) int { return max(0, r.sectionID-1) }

func repoGroup(r row) string {
	switch section(r) {
	case 1:
		return "goals " + r.group
	case 2:
		return "sessions " + r.group
	}
	return "needs"
}

// starts are the indexes of the rows that begin a new part, and the part the row at index at belongs to.
func starts(rows []row, at int, part func(row) string) ([]int, int) {
	var out []int
	cur := 0
	for i, r := range rows {
		if i == 0 || part(r) != part(rows[i-1]) {
			out = append(out, i)
		}
		if i == at {
			cur = len(out) - 1
		}
	}
	return out, cur
}

// move jumps the cursor: tab and shift+tab between sections, [ and ] between repository groups, < > home and end to the top or bottom.
func (m *Model) move(key string) {
	if m.mode == modeAgent {
		switch key {
		case "<", "home":
			m.scrollY, m.cursor, m.agentPaging = 0, 0, true
		case ">", "end":
			m.agentPaging = true
			m.scrollBy(1 << 30)
		}
		return
	}
	rows := m.rows()
	if len(rows) == 0 {
		return
	}
	switch key {
	case "<", "home":
		m.cursor = 0
		return
	case ">", "end":
		m.cursor = len(rows) - 1
		return
	}
	if m.mode != modeList {
		return
	}
	var at []int
	cur := 0
	if key == "tab" || key == "shift+tab" {
		at, cur = starts(rows, m.cursor, func(r row) string { return string(rune('0' + section(r))) })
	} else {
		for i, r := range rows {
			sectionHeader := r.key == "heading:needs" || (r.key == "heading:goals" || r.key == "heading:sessions") && (r.closed || i+1 == len(rows) || rows[i+1].parent != r.key)
			if sectionHeader || r.header != "" && r.group != "" && r.parent == "heading:goals" || r.repo != nil && r.project == nil {
				at = append(at, i)
			}
		}
		for i, index := range at {
			if index <= m.cursor {
				cur = i
			}
		}
	}
	if len(at) == 0 {
		return
	}

	switch key {
	case "tab":
		m.cursor = at[(cur+1)%len(at)]
	case "shift+tab":
		m.cursor = at[(cur+len(at)-1)%len(at)]
	case "]":
		if cur+1 < len(at) {
			m.cursor = at[cur+1]
		}
	case "[":
		m.cursor = at[max(cur-1, 0)]
	}
}

// reply answers the row's open item, else sends a follow-up to the row's session.
func (m Model) reply(r row) (tea.Model, tea.Cmd) {
	if r.item != nil && r.item.State == core.ItemOpen {
		m.startInput(inAnswer, r.item.Key(), "", r.item.Rec, true)
		m.inLabel = r.item.ID
		return m, nil
	}
	s := m.sessionOf(r)
	if s == nil && r.project != nil && len(r.project.Sessions) == 1 {
		s = &r.project.Sessions[0]
	}
	if s == nil || s.ID == "" || m.core.Launches == nil {
		m.message = "nothing here to reply to"
		return m, nil
	}
	m.startInput(inSend, s.Key(), "", "", false)
	m.inLabel = titleOf(*s)
	return m, nil
}
