package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// pickOpt is one manager or chief in the C and c pickers, at its depth in the tree; an empty id is the owner.
type pickOpt struct {
	id    string
	depth int
}

// treeOpts lists the board's managers and chiefs, or only its chiefs, each under its chief, leaving out skip's subtrees.
func (m Model) treeOpts(chiefsOnly bool, skip map[string]bool) []pickOpt {
	var out []pickOpt
	var walk func([]core.Node, int)
	walk = func(ns []core.Node, depth int) {
		for _, n := range ns {
			if skip[n.Manager.ID] || chiefsOnly && n.Goal != nil {
				continue
			}
			out = append(out, pickOpt{n.Manager.ID, depth})
			walk(n.Children, depth+1)
		}
	}
	walk(m.board.Tree, 0)
	return out
}

func (m Model) picking() bool { return m.inKind == inOver || m.inKind == inAttach }

// openOverPicker lists every manager and chief for a new chief to oversee, the selected ones checked.
func (m *Model) openOverPicker() {
	m.opts, m.optCur, m.optOn = m.treeOpts(false, nil), 0, map[string]bool{}
	for id := range m.picked {
		m.optOn[id] = true
	}
	m.startInput(inOver, "", "", "", false)
}

// openChiefPicker lists the chiefs the ids can report to, none of them or below them, after the owner.
func (m *Model) openChiefPicker(ids []string, parent string) {
	skip := map[string]bool{}
	for _, id := range ids {
		skip[id] = true
	}
	m.opts, m.optCur = append([]pickOpt{{}}, m.treeOpts(true, skip)...), 0
	for i, o := range m.opts {
		if o.id == parent {
			m.optCur = i
		}
	}
	m.inPicks = ids
	m.startInput(inAttach, "", "", "", false)
	m.inLabel = strings.Join(ids, ", ")
}

func (m Model) optKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc", "q":
		m.mode = m.back
	case "up", "k":
		m.optCur = max(0, m.optCur-1)
	case "down", "j":
		m.optCur = max(0, min(len(m.opts)-1, m.optCur+1))
	case "space":
		if m.inKind == inOver && m.optCur < len(m.opts) {
			id := m.opts[m.optCur].id
			m.optOn[id] = !m.optOn[id]
		}
	case "enter":
		m.mode = m.back
		if m.inKind == inOver {
			m.inPicks = nil
			for _, o := range m.opts {
				if m.optOn[o.id] {
					m.inPicks = append(m.inPicks, o.id)
				}
			}
			m.startInput(inChief, "", "", "", false)
			m.inLabel = fmt.Sprintf("chief over %s", plural(len(m.inPicks), "report"))
			return m, nil
		}
		if m.optCur >= len(m.opts) {
			return m, nil
		}
		return m.attach(m.inPicks, m.opts[m.optCur].id)
	}
	return m, nil
}

// attach puts every id under chief, or under the owner when chief is "".
func (m Model) attach(ids []string, chief string) (tea.Model, tea.Cmd) {
	m.picked = nil
	return m, m.do(func() (string, error) {
		for _, id := range ids {
			if err := m.core.Attach(id, chief); err != nil {
				return "", err
			}
		}
		if chief == "" {
			return strings.Join(ids, ", ") + " report to you", nil
		}
		return strings.Join(ids, ", ") + " report to " + chief, nil
	})
}

func (m Model) optsView() string {
	var b strings.Builder
	if m.inKind == inOver {
		b.WriteString(pal.bold.Render(" NEW CHIEF") + pal.dim.Render("  check the goals and chiefs it oversees") + "\n")
	} else {
		b.WriteString(pal.bold.Render(" PUT "+m.inLabel+" UNDER") + pal.dim.Render("  the chief it reports to, or you") + "\n")
	}
	if len(m.opts) == 0 {
		b.WriteString(pal.dim.Render("   no goals or chiefs yet; enter starts a chief over none") + "\n")
	}
	room := max(3, m.height-8)
	start := max(0, min(m.optCur-room+2, len(m.opts)-room))
	for i := start; i < len(m.opts) && i < start+room; i++ {
		o := m.opts[i]
		box := ""
		if m.inKind == inOver {
			box = "[ ] "
			if m.optOn[o.id] {
				box = "[x] "
			}
		}
		line := m.markAt(i == m.optCur) + box + strings.Repeat("  ", o.depth) + m.optLabel(o)
		b.WriteString(ansi.Truncate(line, max(20, m.width-1), "…") + "\n")
	}
	if m.inKind == inAttach && len(m.opts) == 1 {
		b.WriteString(pal.dim.Render("   no other chief yet; C starts one") + "\n")
	}
	return b.String()
}

func (m Model) optLabel(o pickOpt) string {
	if o.id == "" {
		return "you" + pal.dim.Render("  report to no chief")
	}
	n, ok := m.board.Node(o.id)
	if !ok {
		return o.id
	}
	stand := standStyle[n.Standing](n.Standing)
	if n.Goal == nil {
		return "chief " + pal.bold.Render(n.Manager.Name) + "  " + stand + pal.dim.Render(fmt.Sprintf("  %s  %d goals", n.Manager.ID, n.Count()))
	}
	return m.goalName(*n.Goal) + "  " + stand + "  " + oneLine(n.Goal.Ask)
}
