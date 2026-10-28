package tui

import (
	"fmt"
	"sort"
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

func (m Model) picking() bool {
	return m.inKind == inOver || m.inKind == inAttach || m.inKind == inChiefMenu
}

// menuOpt is one choice of the c menu and the key that picks it.
type menuOpt struct{ key, label string }

// openChiefMenu offers what c does on r: put its goal or chief under a chief, detach it from its chief, or start a new chief.
func (m *Model) openChiefMenu(r row) {
	m.menu, m.menuFor, m.optCur = nil, "", 0
	if n, ok := m.nodeOf(r); ok {
		name, others := m.ownerName(n.Manager.ID), len(m.picked)
		if m.picked[n.Manager.ID] {
			others--
		}
		if others > 0 {
			name += fmt.Sprintf(" and %d selected", others)
		}
		m.menuFor = n.Manager.ID
		m.menu = append(m.menu, menuOpt{"a", "attach " + name + " to a chief"})
		if n.Manager.ParentID != "" {
			m.menu = append(m.menu, menuOpt{"d", "detach " + m.ownerName(n.Manager.ID) + " from " + m.ownerName(n.Manager.ParentID) + ", so it reports to you"})
		}
	}
	m.menu = append(m.menu, menuOpt{"n", "new chief over the goals and chiefs you check"})
	m.startInput(inChiefMenu, "", "", "", false)
}

func (m Model) chiefMenuKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch key := k.String(); key {
	case "esc", "q":
		m.mode = m.back
	case "up", "k":
		m.optCur = max(0, m.optCur-1)
	case "down", "j":
		m.optCur = max(0, min(len(m.menu)-1, m.optCur+1))
	case "enter":
		return m.chiefMenu(m.menu[m.optCur].key)
	default:
		for _, o := range m.menu {
			if o.key == key {
				return m.chiefMenu(key)
			}
		}
	}
	return m, nil
}

func (m Model) chiefMenu(key string) (tea.Model, tea.Cmd) {
	m.mode = m.back
	n, ok := m.board.Node(m.menuFor)
	switch {
	case key == "n":
		r, _ := m.selected()
		m.newChief(r)
	case key == "a" && ok:
		ids := []string{n.Manager.ID}
		for id := range m.picked {
			if id != n.Manager.ID {
				ids = append(ids, id)
			}
		}
		sort.Strings(ids[1:])
		m.openChiefPicker(ids, n.Manager.ParentID)
	case key == "d" && ok && n.Manager.ParentID != "":
		m.detach(n)
	}
	return m, nil
}

// newChief opens the picker of a new chief's reports, starting it in r's repository.
func (m *Model) newChief(r row) {
	m.newL = core.NewLaunch{Repo: core.MainCheckout(m.repoOf(r))}
	m.loadTarget()
	m.targetRepo()
	m.loadHarness("chief")
	m.openOverPicker()
}

// detach asks y/n, then has n report to the owner instead of its chief.
func (m *Model) detach(n core.Node) {
	id, chief := n.Manager.ID, n.Manager.ParentID
	if c, ok := m.board.Node(chief); ok {
		chief = c.Manager.Label()
	}
	m.confirm("detach "+n.Manager.Label()+" from "+chief+"? it reports to you", m.do(func() (string, error) {
		return id + " reports to you", m.core.Attach(id, "")
	}))
}

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
	m.inLabel = m.ownerNames(ids)
}

// ownerName is a manager as the owner knows it: a goal's id, or "chief" and the chief's name.
func (m Model) ownerName(id string) string {
	n, ok := m.board.Node(id)
	switch {
	case !ok:
		return id
	case n.Goal != nil:
		return m.goalName(*n.Goal)
	}
	return "chief " + n.Manager.Name
}

func (m Model) ownerNames(ids []string) string {
	names := make([]string, len(ids))
	for i, id := range ids {
		names[i] = m.ownerName(id)
	}
	return strings.Join(names, ", ")
}

func (m Model) optKey(k tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.inKind == inChiefMenu {
		return m.chiefMenuKey(k)
	}
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
	names, to := m.ownerNames(ids), m.ownerName(chief)
	return m, m.do(func() (string, error) {
		for _, id := range ids {
			if err := m.core.Attach(id, chief); err != nil {
				return "", err
			}
		}
		if chief == "" {
			return names + " report to you", nil
		}
		return names + " report to " + to, nil
	})
}

func (m Model) optsView() string {
	var b strings.Builder
	if m.inKind == inChiefMenu {
		b.WriteString(pal.bold.Render(" CHIEF") + pal.dim.Render("  chiefs oversee goals and other chiefs") + "\n")
		for i, o := range m.menu {
			b.WriteString(ansi.Truncate(m.markAt(i == m.optCur)+pal.bold.Render(o.key)+"  "+o.label, max(20, m.width-1), "…") + "\n")
		}
		return b.String()
	}
	if m.inKind == inOver {
		b.WriteString(pal.bold.Render(" NEW CHIEF") + pal.dim.Render("  check the goals and chiefs it oversees") + "\n")
	} else {
		b.WriteString(pal.bold.Render(" PUT "+m.inLabel+" UNDER") + pal.dim.Render("  "+strings.Join(m.inPicks, ", ")+"  the chief it reports to, or you") + "\n")
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
		b.WriteString(pal.dim.Render("   no other chief yet; c then n starts one") + "\n")
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
