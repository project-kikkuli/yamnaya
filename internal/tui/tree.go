package tui

import (
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"sort"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type treeNode struct {
	row      row
	children []treeNode
}

func treeSessionKey(s core.Live) string {
	key := s.Ref()
	if s.Remote != "" {
		return core.RemoteKey(s.Remote, key)
	}
	return key
}

func heading(key, title string, children []treeNode) treeNode {
	return treeNode{row: row{key: "heading:" + key, header: title, container: true}, children: children}
}

func textSection(key, title, body string) treeNode {
	n := heading(key, title, nil)
	n.row.body = body
	return n
}

func (m Model) closed(r row) bool {
	if m.filter != "" {
		return false
	}
	if closed, ok := m.folds[rowKey(r)]; ok {
		return closed
	}
	if r.stale {
		return !m.showStale
	}
	return r.initialClosed
}

func (m *Model) setFold(r row, closed bool) {
	if m.filter != "" {
		return
	}
	if m.folds == nil {
		m.folds = map[string]bool{}
	}
	m.folds[rowKey(r)] = closed
}

func (m Model) projectRows(all bool) []row { return m.projectNodes(m.uiTree(), all) }

func (m Model) projectNodes(nodes []treeNode, all bool) []row {
	var out []row
	var walk func([]treeNode, string, int, int, string)
	walk = func(nodes []treeNode, parent string, depth, section int, group string) {
		for _, n := range nodes {
			r := n.row
			r.parent, r.depth = parent, depth
			if r.item != nil && r.need == nil {
				r.key = rowKey(r) + "/in:" + parent
			}
			if r.sectionID != 0 {
				section = r.sectionID
			}
			r.sectionID = section
			if r.group != "" {
				group = r.group
			}
			r.group = group
			r.container = r.container || len(n.children) > 0
			r.closed = m.closed(r)
			out = append(out, r)
			if all || !r.closed {
				walk(n.children, rowKey(r), depth+1, section, group)
			}
		}
	}
	walk(nodes, "", 0, 0, "")
	return out
}

func (m *Model) firstContent() {
	for i, r := range m.rows() {
		if r.header == "" {
			m.cursor = i
			return
		}
	}
	m.cursor = 0
}

func (m *Model) treeMove(key string) { m.treeMoveRows(key, m.rows()) }

func (m *Model) treeMoveRows(key string, rows []row) {
	m.scrollY = 0
	if m.cursor < 0 || m.cursor >= len(rows) {
		return
	}
	r := rows[m.cursor]
	if key == "left" {
		if r.container && !r.closed && m.filter == "" {
			m.setFold(r, true)
			return
		}
		for i, p := range rows {
			if rowKey(p) == r.parent {
				m.cursor = i
				return
			}
		}
	} else if r.container {
		if r.closed {
			m.setFold(r, false)
		} else if m.cursor+1 < len(rows) && rows[m.cursor+1].parent == rowKey(r) {
			m.cursor++
		}
	}
}

func (m Model) uiTree() []treeNode {
	nodes := m.boardTree()
	if m.filter == "" || m.listMode() != modeList && m.listMode() != modeChief {
		return nodes
	}
	inherited := false
	if m.listMode() == modeChief {
		for _, n := range nodePath(m.board.Tree, m.chief) {
			if m.matches(nodeText(n), m.hostText(n.Manager.Host)) {
				inherited = true
				break
			}
		}
	}
	return m.filterTree(nodes, inherited)
}

func (m Model) boardTree() []treeNode {
	switch m.listMode() {
	case modeAgent:
		return m.agentSections()
	case modeItems:
		return itemTree(m.items, nil)
	case modeRepo:
		r, ok := m.board.Repo(m.repo)
		if !ok {
			return nil
		}
		var missions []treeNode
		for i := range r.Missions {
			missions = append(missions, m.missionTree(&r.Missions[i], &r))
		}
		return []treeNode{heading("repo/"+r.Key+"/missions", "MISSIONS", missions)}
	case modeDetail:
		p, ok := m.board.Project(m.detail)
		if !ok {
			return nil
		}
		return m.missionChildren(&p)
	case modeSession:
		s, ok := m.session()
		if !ok {
			return nil
		}
		out := m.sessionSections(*s)
		out = append(out, heading("session/"+treeSessionKey(*s)+"/crew", fmt.Sprintf("CREW (%d)", len(s.Crew)), m.crewTree(s, nil)))
		if p, ok := m.board.Project(m.detail); ok {
			var items []core.Item
			for _, it := range p.Items {
				if it.Session == s.Key() {
					items = append(items, it)
				}
			}
			if len(items) > 0 {
				out = append(out, heading("session/"+treeSessionKey(*s)+"/items", "ITEMS", itemTree(items, &p)))
			}
		}
		return out
	case modeGoal:
		n, ok := m.board.Node(m.goal)
		if !ok {
			return nil
		}
		out := m.goalSections(n)
		out = append(out, heading("node/"+n.Manager.ID+"/sessions", "SESSIONS", m.nodeSessions(n)))
		return out
	case modeChief:
		n, ok := m.board.Node(m.chief)
		if !ok {
			return nil
		}
		return []treeNode{heading("node/"+n.Manager.ID+"/reports", fmt.Sprintf("REPORTS (%d)", len(n.Children)), m.nodesTree(n.Children, chiefGroup))}
	}
	needs := []treeNode{}
	for i := range m.board.Needs {
		n := &m.board.Needs[i]
		r := row{need: n, item: n.Item, session: n.Session, initialClosed: true}
		t := treeNode{row: r}
		if len(n.Asks) > 1 {
			t.children = itemTree(n.Asks, nil)
		}
		if n.Doc != nil && len(n.Docs) > 1 {
			for j := range n.Docs {
				d := &n.Docs[j]
				child := core.Need{Doc: d, Docs: []core.DocAsk{*d}, Project: n.Project, Manager: n.Manager}
				t.children = append(t.children, treeNode{row: row{key: "doc-ask:" + d.Key, need: &child}})
			}
		}
		needs = append(needs, t)
	}
	if len(m.board.Stale) > 0 {
		stale := treeNode{row: row{stale: true, container: true, initialClosed: true}}
		for i := range m.board.Stale {
			n := &m.board.Stale[i]
			stale.children = append(stale.children, treeNode{row: row{need: n, item: n.Item}})
		}
		needs = append(needs, stale)
	}
	var tops []core.Node
	if m.flat {
		var walk func([]core.Node)
		walk = func(ns []core.Node) {
			for _, n := range ns {
				if n.Goal != nil {
					top := n
					top.Children = nil
					tops = append(tops, top)
				}
				walk(n.Children)
			}
		}
		walk(m.board.Tree)
	} else {
		tops = append(tops, m.board.Tree...)
	}
	sort.SliceStable(tops, func(i, j int) bool { return groupLess(goalGroup(tops[i]), goalGroup(tops[j])) })
	var groups []treeNode
	for _, n := range tops {
		g := goalGroup(n)
		if len(groups) == 0 || groups[len(groups)-1].row.group != g {
			h := heading("goals/"+g, groupLabel(g), nil)
			h.row.group = g
			groups = append(groups, h)
		}
		at := len(groups) - 1
		groups[at].children = append(groups[at].children, m.nodesTree([]core.Node{n}, g)...)
	}
	var repos []treeNode
	for i := range m.board.Repos {
		r := &m.board.Repos[i]
		var missions []treeNode
		for j := range r.Missions {
			if r.Missions[j].Manager == "" {
				missions = append(missions, m.missionTree(&r.Missions[j], r))
			}
		}
		if len(missions) > 0 {
			repos = append(repos, treeNode{row: row{repo: r, group: "repo " + r.Key}, children: missions})
		}
	}
	out := []treeNode{heading("needs", fmt.Sprintf("NEEDS YOU (%d)", len(m.board.Needs)), needs), heading("goals", "GOALS", groups), heading("sessions", "SESSIONS YOU STARTED", repos)}
	for i := range out {
		out[i].row.sectionID = i + 1
	}
	return out
}

func (m Model) nodesTree(ns []core.Node, group string) []treeNode {
	var out []treeNode
	for i := range ns {
		n := &ns[i]
		children := m.nodeSessions(*n)
		children = append(children, m.nodesTree(n.Children, group)...)
		out = append(out, treeNode{row: row{node: n, group: group, initialClosed: n.Goal != nil}, children: children})
	}
	return out
}

func (m Model) nodeSessions(n core.Node) []treeNode {
	sessions := append([]core.Live{}, n.Agents...)
	if n.Live != nil {
		sessions = append([]core.Live{*n.Live}, sessions...)
	}
	for i := range sessions {
		if parent := m.board.ParentOf(treeSessionKey(sessions[i])); parent != "" {
			sessions[i].Parent = parent
		}
	}
	if n.Live != nil {
		for i := range sessions {
			if sessions[i].Key() != n.Live.Key() && sessions[i].Parent == "" {
				sessions[i].Parent = treeSessionKey(*n.Live)
			}
		}
	}
	return m.sessionsTree(sessions, nil)
}

func (m Model) missionTree(p *core.Project, r *core.Repo) treeNode {
	return treeNode{row: row{project: p, repo: r, initialClosed: true}, children: m.missionChildren(p)}
}

func (m Model) missionChildren(p *core.Project) []treeNode {
	sessions := m.sessionsTree(p.Sessions, p)
	for i := range p.Dormant {
		sessions = append(sessions, treeNode{row: row{dormant: &p.Dormant[i], project: p}})
	}
	return []treeNode{heading("project/"+p.Key+"/sessions", "SESSIONS AND CREW", sessions), heading("project/"+p.Key+"/items", "ITEMS", itemTree(p.Items, p))}
}

func itemTree(items []core.Item, p *core.Project) []treeNode {
	var out []treeNode
	for i := range items {
		out = append(out, treeNode{row: row{item: &items[i], project: p}})
	}
	return out
}

func (m Model) crewTree(s *core.Live, p *core.Project) []treeNode {
	var out []treeNode
	for i := range s.Crew {
		out = append(out, treeNode{row: row{member: &s.Crew[i], session: s, project: p}})
	}
	return out
}

func (m Model) sessionParentKey(s core.Live) string {
	parent := s.Parent
	if parent == "" {
		parent = m.board.ParentOf(treeSessionKey(s))
	}
	if parent != "" && s.Remote != "" && !strings.HasPrefix(parent, s.Remote+"/") {
		parent = core.RemoteKey(s.Remote, parent)
	}
	return parent
}

func (m Model) sessionsTree(sessions []core.Live, p *core.Project) []treeNode {
	byKey := map[string]*core.Live{}
	aliases := map[string]*core.Live{}
	for i := range sessions {
		s := &sessions[i]
		byKey[treeSessionKey(*s)] = s
		key := sessionKey(*s)
		if _, exists := aliases[key]; exists {
			aliases[key] = nil
		} else {
			aliases[key] = s
		}
	}
	resolve := func(key string) *core.Live {
		if s := byKey[key]; s != nil {
			return s
		}
		return aliases[key]
	}
	children := map[string][]*core.Live{}
	var roots []*core.Live
	for i := range sessions {
		s := &sessions[i]
		parent := resolve(m.sessionParentKey(*s))
		seen := map[string]bool{treeSessionKey(*s): true}
		for a := parent; a != nil; a = resolve(m.sessionParentKey(*a)) {
			if seen[treeSessionKey(*a)] {
				parent = nil
				break
			}
			seen[treeSessionKey(*a)] = true
		}
		if parent == nil {
			roots = append(roots, s)
		} else {
			children[treeSessionKey(*parent)] = append(children[treeSessionKey(*parent)], s)
		}
	}
	var walk func([]*core.Live) []treeNode
	walk = func(ss []*core.Live) []treeNode {
		var out []treeNode
		for _, s := range ss {
			nested := walk(children[treeSessionKey(*s)])
			crew := m.crewTree(s, p)
			if len(crew) > 0 {
				nested = append(nested, heading("session/"+treeSessionKey(*s)+"/crew", fmt.Sprintf("CREW (%d)", len(crew)), crew))
			}
			out = append(out, treeNode{row: row{session: s, project: p}, children: nested})
		}
		return out
	}
	return walk(roots)
}

func (m Model) filterTree(ns []treeNode, inherited bool) []treeNode {
	var out []treeNode
	for _, n := range ns {
		r := n.row
		hit := inherited || m.matches(m.treeText(r))
		n.children = m.filterTree(n.children, hit)
		if hit || len(n.children) > 0 {
			out = append(out, n)
		}
	}
	return out
}

func (m Model) treeText(r row) string {
	switch {
	case r.header != "":
		return r.header
	case r.item != nil && r.need == nil:
		return r.item.Question
	case r.need != nil:
		return m.projectName(r.need.Project) + " " + needText(*r.need) + " " + m.hostText(m.needHost(*r.need))
	case r.node != nil:
		return nodeText(*r.node) + " " + m.hostText(r.node.Manager.Host)
	case r.member != nil:
		return r.member.Name + " " + r.member.Brief
	case r.session != nil:
		return titleOf(*r.session) + " " + r.session.Task + " " + m.hostText(r.session.Remote)
	case r.project != nil:
		return r.project.Name + " " + m.hostText(m.projectHost(*r.project))
	case r.repo != nil:
		return r.repo.Name
	case r.item != nil:
		return r.item.Question
	}
	return ""
}

func (m Model) sceneNodeRows(ns []core.Node, group string, depth int) []row {
	var out []row
	for i := range ns {
		n := &ns[i]
		if m.matches(nodeText(*n), m.hostText(n.Manager.Host)) || m.treeMatches(n.Children) {
			out = append(out, row{node: n, group: group, depth: depth})
			out = append(out, m.sceneNodeRows(n.Children, group, depth+1)...)
		}
	}
	return out
}

func (m Model) treePage() viewPage { return m.renderTree(m.rows()) }

func (m Model) renderTree(rows []row) viewPage {
	b := viewPage{}
	var items []core.Item
	for _, r := range rows {
		if r.item != nil {
			items = append(items, *r.item)
		}
	}
	ids := itemIDs(items)
	for i, r := range rows {
		rowModel := m
		rowModel.width = max(20, m.width-2*r.depth-2)
		text := rowModel.treeRowText(i, r, ids)
		lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
		indicator := "  "
		if r.container {
			indicator = "▾ "
			if r.closed {
				indicator = "▸ "
			}
		}
		indent := strings.Repeat("  ", r.depth)
		for j, line := range lines {
			if j == 0 {
				mark := m.mark(i)
				if r.node != nil && m.picked[r.node.Manager.ID] && !m.dyingAt[i] {
					mark = mark[:len(mark)-1] + "*"
				}
				line = mark + indent + indicator + ansi.Cut(line, 3, ansi.StringWidth(line))
			} else {
				line = indent + "  " + line
			}
			b.place = m.rowPlace(r)
			b.WriteString(line + "\n")
		}
		if r.header != "" && !r.closed {
			if r.body != "" {
				b.WriteString(r.body)
			} else if !r.container || (i+1 == len(rows) || rows[i+1].parent != rowKey(r)) {
				b.WriteString(pal.dim.Render(indent+"     none") + "\n")
			}
		}
	}
	return b
}

func (m Model) rowPlace(r row) string {
	switch {
	case r.node != nil:
		return "g:" + r.node.Manager.ID
	case r.member != nil:
		return "a:" + r.session.Key() + "/" + r.member.ID
	case r.session != nil:
		return "s:" + r.session.Key()
	case r.project != nil:
		return "m:" + r.project.Key
	case r.repo != nil:
		return "r:" + r.repo.Key
	}
	return ""
}

func (m Model) treeRowText(i int, r row, ids map[string]string) string {
	switch {
	case r.header != "":
		return m.mark(i) + pal.bold.Render(r.header) + "\n"
	case r.stale:
		return m.mark(i) + fmt.Sprintf("stale (%d) open items whose sessions ended\n", len(m.board.Stale))
	case r.node != nil:
		return withNote(m.nodeLines(i, *r.node, 0), m.rowNote(r))
	case r.need != nil:
		if text, ok := m.queueLines(i, *r.need); ok {
			return text
		}
		return withNote(m.needLines(i, *r.need), m.rowNote(r))
	case r.member != nil:
		return m.crewLine(i, *r.session, *r.member)
	case r.session != nil:
		s := *r.session
		line := m.mark(i) + pad(titleOf(s), m.nameWidth()) + " " + pad(profileDisplay(s.Harness, s.Profile), 7) + pal.state(stateWord(s)).Render(pad(stateWord(s), 9)) + m.where(s)
		if s.Branch != "" {
			line += " " + s.Branch
		}
		if sp := m.spend(s); sp != "" {
			line += "  " + sp
		}
		if s.Prompt != nil {
			line += "  " + pal.yellow.Render(s.Prompt.String())
		}
		if len(s.Copies) > 1 {
			line += "  " + pal.red.Render(fmt.Sprintf("%d COPIES", len(s.Copies)))
		}
		if i == m.cursor && s.Adoptable() {
			line += pal.dim.Render("   M move in")
		}
		if d := doingText(m.board.At, s.Session); d != "" {
			line += "\n" + pal.dim.Render("     now: ") + d
		}
		return withNote(line+"\n", m.rowNote(r))

	case r.dormant != nil:
		d := r.dormant
		return withNote(m.mark(i)+pad(d.Title, m.nameWidth())+" "+d.Harness+" "+d.State+" "+d.Key+" "+d.Branch+"  enter revive\n", m.note(d.Key))
	case r.item != nil:
		if m.listMode() == modeList {
			return m.needLines(i, core.Need{Item: r.item, Project: m.board.ProjectOf(r.item.Session)})
		}
		return m.itemLines(i, *r.item, ids)
	case r.project != nil:
		return withNote(m.missionLine(i, *r.project, m.missionCols(2)), m.rowNote(r))
	case r.repo != nil:
		return m.repoLine(i, *r.repo, m.missionCols(2))
	}
	return ""
}
