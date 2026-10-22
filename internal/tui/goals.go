package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func openInBrowser(url string) error { return exec.Command("open", url).Start() }

// nodeOf is the manager or chief a row stands for: a goal or chief row, a goal's queue row, or the goal or chief view itself.
func (m Model) nodeOf(r row) (core.Node, bool) {
	switch {
	case r.node != nil:
		return *r.node, true
	case r.need != nil && r.need.Goal != nil:
		return m.board.Node(r.need.Goal.ManagerID)
	case r.need != nil && r.need.Steer != nil:
		return m.board.Node(r.need.Manager)
	}
	switch m.mode {
	case modeGoal:
		return m.board.Node(m.goal)
	case modeChief:
		return m.board.Node(m.chief)
	}
	return core.Node{}, false
}

// treeRows are the goal rows of the board: chiefs collapsed to one row, or every manager by repository.
func (m Model) treeRows() []row {
	var out []row
	if m.byRepo {
		var all []core.Node
		var walk func([]core.Node)
		walk = func(ns []core.Node) {
			for _, n := range ns {
				if n.Goal != nil {
					all = append(all, n)
				}
				walk(n.Children)
			}
		}
		walk(m.board.Tree)
		sort.SliceStable(all, func(i, j int) bool {
			if a, b := core.RepoLabel(all[i].Repo), core.RepoLabel(all[j].Repo); a != b {
				return a < b
			}
			return all[i].Repo < all[j].Repo
		})
		for i := range all {
			if m.matches(all[i].Goal.Ask, all[i].Manager.Name) {
				out = append(out, row{node: &all[i]})
			}
		}
		return out
	}
	for i := range m.board.Tree {
		n := &m.board.Tree[i]
		if m.matches(nodeText(*n)) {
			out = append(out, row{node: n})
		}
	}
	return out
}

func agentCount(n core.Node) int {
	total := len(n.Agents)
	for _, c := range n.Children {
		total += agentCount(c)
	}
	return total
}

func nodeText(n core.Node) string {
	if n.Goal != nil {
		return n.Goal.Ask + " " + n.Manager.Name
	}
	return n.Manager.Name
}

// queueRows are the owner's queue, with every one of a session's folded asks listed under it while it is open.
func (m Model) queueRows() []row {
	var out []row
	for i := range m.board.Needs {
		n := &m.board.Needs[i]
		if !m.matches(m.projectName(n.Project), needText(*n)) {
			continue
		}
		out = append(out, row{need: n, item: n.Item, session: n.Session})
		if len(n.Asks) > 1 && m.unfolded[n.Key()] {
			for j := range n.Asks {
				out = append(out, row{item: &n.Asks[j]})
			}
		}
	}
	return out
}

func (m Model) goalRows() []row {
	n, ok := m.board.Node(m.goal)
	if !ok {
		return nil
	}
	var out []row
	if n.Live != nil {
		out = append(out, row{session: n.Live})
	}
	for i := range n.Agents {
		out = append(out, row{session: &n.Agents[i]})
	}
	return out
}

func (m Model) chiefRows() []row {
	n, ok := m.board.Node(m.chief)
	if !ok {
		return nil
	}
	out := make([]row, 0, len(n.Children))
	for i := range n.Children {
		out = append(out, row{node: &n.Children[i]})
	}
	return out
}

var standStyle = map[string]func(string) string{
	core.StandDone:      func(s string) string { return pal.green.Render(s) },
	core.StandVerifying: func(s string) string { return pal.yellow.Render(s) },
	core.StandMoving:    func(s string) string { return s },
	core.StandQuiet:     func(s string) string { return pal.dim.Render(s) },
	core.StandAdrift:    func(s string) string { return pal.red.Render(s) },
	core.StandDown:      func(s string) string { return pal.red.Render(s) },
	core.StandStuck:     func(s string) string { return pal.red.Render(s) },
}

// nodeLines is a goal or chief row: who owns it, how it stands, and what it is about.
func (m Model) nodeLines(i int, n core.Node) string {
	mark := m.mark(i)
	if m.picked[n.Manager.ID] {
		mark = mark[:len(mark)-1] + "*"
	}
	stand := standStyle[n.Standing](pad(n.Standing, 7))
	var head, what string
	host := n.Manager.Host
	if n.Goal != nil {
		_, id, _ := core.SplitRemote(n.Goal.ID)
		head = pad(id, 8)
		what = m.tagged(host, n.Goal.Ask)
		if m.byRepo {
			what = core.RepoLabel(n.Repo) + "  " + what
		}
	} else {
		head = pad("chief", 8)
		what = m.tagged(host, pal.bold.Render(n.Manager.Name)+pal.dim.Render(fmt.Sprintf("  %d goals", n.Count())))
	}
	agents := pal.dim.Render(pad(plural(agentCount(n), "agent"), 9))
	if u := n.Unread(); u > 0 {
		agents += pal.yellow.Render(fmt.Sprintf("%d new ", u))
	}
	if n.Feed.Latest != nil {
		what += pal.dim.Render("  " + n.Feed.Latest.Payload)
	}
	line := mark + head + " " + stand + " " + agents + " " + planTags(n)
	b := line + ansi.Truncate(oneLine(what), max(10, m.width-ansi.StringWidth(line)-1), "…") + "\n"
	if i != m.cursor {
		return b
	}
	var meta []string
	if n.Goal != nil {
		meta = append(meta, n.Manager.ID, "moved "+age(m.board.At, n.Goal.LastMovedAt))
		if n.Goal.Evidence != "" {
			meta = append(meta, n.Goal.Evidence)
		}
	} else {
		meta = append(meta, n.Manager.ID)
	}
	if n.Manager.LastError != "" {
		meta = append(meta, "launch: "+n.Manager.LastError)
	}
	if n.Goal == nil || n.Goal.State != core.GoalDone {
		if f := n.Fresh; f.Stale() {
			meta = append(meta, pal.yellow.Render(briefMeta(m.board.At, n)))
		} else {
			meta = append(meta, briefMeta(m.board.At, n))
		}
	}
	if state := m.hostState(host); state != "" {
		meta = append(meta, "on "+host+": "+state)
	}
	keys := "enter open   B brief   W brief page   o session   s send   S steer plan   space select   R relaunch"
	if n.Goal != nil {
		keys += "   K drop"
	}
	if brief, ok := m.artifact(n); ok && brief != "" {
		keys += "   V artifact"
	}
	return b + pal.dim.Render("         "+strings.Join(meta, " | ")) + "\n" + pal.dim.Render("         "+keys) + "\n"
}

// planTags flag a steer the manager has not adopted, so it shows at a glance; the brief's freshness shows on the selected row.
func planTags(n core.Node) string {
	if n.Steer == nil || n.Steer.Adopted() {
		return ""
	}
	return pal.yellow.Render(fmt.Sprintf("steer r%d not adopted", n.Steer.Revision)) + "  "
}

func briefMeta(at time.Time, n core.Node) string {
	f := n.Fresh
	switch {
	case f.Brief == nil:
		return "no brief yet"
	case f.Stale():
		return fmt.Sprintf("brief v%d %s, predates %s", f.Brief.Version, age(at, f.Brief.CreatedAt), f.BehindWhat)
	}
	return fmt.Sprintf("brief v%d %s", f.Brief.Version, age(at, f.Brief.CreatedAt))
}

func planField(n core.Node) string {
	st := n.Steer
	if st == nil {
		return ""
	}
	state := "adopted " + st.AdoptedAt.Local().Format(time.Kitchen)
	if !st.Adopted() {
		state = pal.yellow.Render("not adopted")
	}
	return fmt.Sprintf("r%d by %s, %s: %s", st.Revision, st.By(), state, oneLine(st.Text))
}

func (m Model) artifact(n core.Node) (string, bool) {
	if n.Goal != nil && n.Goal.ArtifactURL != "" {
		return n.Goal.ArtifactURL, true
	}
	return n.Manager.ArtifactURL, n.Manager.ArtifactURL != ""
}

func briefOf(n core.Node) string {
	if n.Goal != nil {
		return n.Goal.BriefPath
	}
	return n.Manager.BriefPath
}

// queueLines draws the owner duties that are not an item or a session: finished and stuck goals, merges, reviews and doc asks.
func (m Model) queueLines(i int, n core.Need) (string, bool) {
	sel := i == m.cursor
	var head, what, keys string
	switch {
	case n.Goal != nil && n.Goal.State == core.GoalDone:
		head, what = pal.green.Render(pad("done", 8)), n.Goal.ID+" "+n.Goal.Ask
		if n.Goal.Summary != "" {
			what += pal.dim.Render("  " + n.Goal.Summary)
		}
		keys = "B brief   o manager   x clear"
	case n.Goal != nil:
		head, what = pal.red.Render(pad("stuck", 8)), n.Goal.ID+" "+n.Goal.Ask
		keys = "enter open   B brief   o manager   R relaunch   s send   x clear"
	case n.Steer != nil:
		head = pal.yellow.Render(pad("steer", 8))
		what = fmt.Sprintf("%s has not adopted r%d after %d nudges: %s", n.Manager, n.Steer.Revision, n.Steer.Nudges, n.Steer.Text)
		keys = "enter open   o manager   S steer again   R relaunch   x dismiss"
	case n.PR != nil:
		head, what = pal.yellow.Render(pad("merge", 8)), n.PR.URL
		if n.PR.Status != nil && n.PR.Status.Title != "" {
			what = n.PR.Status.Title + pal.dim.Render("  "+n.PR.URL)
		}
		keys = "enter open in browser   x dismiss"
	case n.Review != nil:
		head, what = pal.yellow.Render(pad("review", 8)), n.Review.URL
		keys = "enter open in browser   x dismiss"
	case n.Doc != nil:
		head, what = pal.yellow.Render(pad("doc", 8)), n.Doc.Title+pal.dim.Render("  "+n.Doc.Asker+": "+n.Doc.Text)
		if len(n.Docs) > 1 {
			head, what = pal.yellow.Render(pad("doc", 8)), fmt.Sprintf("%s  %d open threads", n.Doc.Title, len(n.Docs))+pal.dim.Render("  newest "+n.Doc.Asker+": "+n.Doc.Text)
		}
		keys = "enter open in browser   x dismiss"
	default:
		return "", false
	}
	line := m.mark(i) + head + " "
	b := line + ansi.Truncate(oneLine(what), max(10, m.width-ansi.StringWidth(line)-1), "…") + "\n"
	if sel {
		if n.Goal != nil && n.Goal.Evidence != "" {
			b += pal.dim.Render("         "+n.Goal.Evidence) + "\n"
		}
		for _, d := range n.Docs {
			lines := []string{d.Asker + ": " + d.Text, d.URL}
			if len(n.Docs) == 1 {
				lines = append(strings.Split(d.Evidence, "\n"), d.URL)
			}
			for _, l := range lines {
				b += pal.dim.Render("         "+ansi.Truncate(oneLine(l), max(10, m.width-10), "…")) + "\n"
			}
		}
		b += pal.dim.Render("         "+keys) + "\n"
	}
	return b, true
}

func (m Model) goalView() viewPage {
	n, ok := m.board.Node(m.goal)
	if !ok {
		return textPage(" that goal left the board\n", "")
	}
	b := viewPage{}
	g := n.Goal
	b.WriteString(fmt.Sprintf(" %s  %s  %s  %s\n", pal.bold.Render("GOAL "+g.ID), standStyle[n.Standing](n.Standing), filepath.Base(g.Repo), pal.dim.Render("moved "+age(m.board.At, g.LastMovedAt))))
	b.WriteString(" " + ansi.Wrap(g.Ask, max(20, m.width-2), " ") + "\n")
	m.field(&b, "manager", n.Manager.ID+" "+managerState(m.board, n))
	m.field(&b, "plan", planField(n))
	m.field(&b, "brief", briefMeta(m.board.At, n)+"  "+core.BriefURL(n.Manager.ID)+pal.dim.Render("  B shows it, W opens the page"))
	if url, ok := m.artifact(n); ok {
		m.field(&b, "artifact", url+pal.dim.Render("  V opens it"))
	}
	if g.Evidence != "" {
		m.field(&b, "stuck", g.Evidence)
	}
	if g.Summary != "" {
		m.field(&b, "done", g.Summary)
	}
	b.WriteString("\n" + pal.bold.Render(" FEED") + pal.dim.Render("  newest first") + "\n")
	if len(n.Feed.Recent) == 0 {
		b.WriteString(pal.dim.Render("   nothing yet; ym records PRs, CI, merges, agent replies and the manager's updates here") + "\n")
	}
	for _, e := range n.Feed.Recent[:min(len(n.Feed.Recent), 12)] {
		line := " " + pal.dim.Render(age(m.board.At, e.Created)) + " " + pad(e.Kind, 14) + " " + oneLine(e.Payload)
		if !e.OwnerNews() {
			line = pal.dim.Render(line)
		} else if e.Seen.IsZero() || !e.Seen.Before(m.feedFrom) {
			line = pal.yellow.Render("*") + line
		}
		b.WriteString(ansi.Truncate(line, max(20, m.width-1), "…") + "\n")
	}
	b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" SESSIONS (%d)", len(m.rows()))) + "\n")
	for i, r := range m.rows() {
		label := ""
		if r.session.Key() == n.Manager.Session {
			label = pal.dim.Render("manager  ")
		}
		b.WriteString(m.mark(i) + label + pad(titleOf(*r.session), 36) + " " + pal.state(stateWord(*r.session)).Render(pad(stateWord(*r.session), 8)) + " " + doingText(m.board.At, r.session.Session) + "\n")
	}
	return b
}

func managerState(b core.Board, n core.Node) string {
	m := n.Manager
	switch {
	case n.Live != nil:
		return "runs as " + core.Short(m.Session) + ", " + n.Live.Status.String()
	case m.LastError != "":
		return "down: " + m.LastError
	case m.Session != "" && b.Alive(m):
		return "starting as " + core.Short(m.Session)
	}
	return "down; ym relaunches it"
}

func (m Model) chiefView() viewPage {
	n, ok := m.board.Node(m.chief)
	if !ok {
		return textPage(" that chief left the board\n", "")
	}
	b := viewPage{}
	b.WriteString(fmt.Sprintf(" %s  %s  %s\n", pal.bold.Render("CHIEF "+n.Manager.Name), standStyle[n.Standing](n.Standing), pal.dim.Render(n.Manager.ID)))
	m.field(&b, "session", managerState(m.board, n))
	m.field(&b, "plan", planField(n))
	m.field(&b, "brief", briefMeta(m.board.At, n)+"  "+core.BriefURL(n.Manager.ID)+pal.dim.Render("  B shows it, W opens the page with its reports' briefs"))
	b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" REPORTS (%d)", len(n.Children))) + "\n")
	if len(n.Children) == 0 {
		b.WriteString(pal.dim.Render("   none; c on a goal row puts it under a chief") + "\n")
	}
	for i, r := range m.rows() {
		b.WriteString(m.nodeLines(i, *r.node))
	}
	return b
}

// steerTargets are the managers and chiefs s sends to: every picked one, else the row's.
func (m Model) steerTargets(r row) []core.Manager {
	var out []core.Manager
	for id := range m.picked {
		if n, ok := m.board.Node(id); ok {
			out = append(out, n.Manager)
		}
	}
	if len(out) == 0 {
		if n, ok := m.nodeOf(r); ok {
			out = append(out, n.Manager)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// goalKey handles the goal tree's keys and the owner queue's goal, merge and review rows; handled is false for every other key.
func (m Model) goalKey(k tea.KeyPressMsg) (Model, tea.Cmd, bool) {
	key := k.String()
	r, _ := m.selected()
	switch key {
	case "n", "N":
		if m.core.Launches == nil {
			m.message = "ym's launcher is unavailable"
			return m, nil, true
		}
		m.newL = core.NewLaunch{Harness: "claude"}
		m.loadTarget()
		if key == "N" {
			m.forGoal = true
			m.openRepoPicker()
			return m, m.scanRepos(), true
		}
		m.newL.Repo = core.MainCheckout(m.repoOf(r))
		m.startInput(inGoal, "", "", "", false)
		return m, nil, true
	case "+":
		if m.core.Launches == nil {
			m.message = "ym's launcher is unavailable"
			return m, nil, true
		}
		m.forGoal = false
		m.newL = core.NewLaunch{Harness: "claude", Repo: core.MainCheckout(m.repoOf(r))}
		m.loadTarget()
		m.startInput(inNew, "", "", "", false)
		return m, nil, true
	case "t":
		if m.mode == modeList {
			m.byRepo = !m.byRepo
			return m, nil, true
		}
	case "C":
		var over []string
		for id := range m.picked {
			over = append(over, id)
		}
		sort.Strings(over)
		m.inPicks = over
		m.newL = core.NewLaunch{Repo: core.MainCheckout(m.repoOf(r))}
		m.loadTarget()
		m.startInput(inChief, "", "", "", false)
		m.inLabel = fmt.Sprintf("new chief over %d", len(over))
		return m, nil, true
	}
	if r.need != nil && (r.need.PR != nil || r.need.Review != nil || r.need.Doc != nil) {
		url := ""
		switch {
		case r.need.PR != nil:
			url = r.need.PR.URL
		case r.need.Review != nil:
			url = r.need.Review.URL
		default:
			url = r.need.Doc.URL
			if len(r.need.Docs) > 1 {
				url, _, _ = strings.Cut(url, "?d=")
			}
		}
		switch key {
		case "enter", "o":
			open := m.openURL
			return m, m.do(func() (string, error) { return "opened " + url, open(url) }), true
		case "x":
			keys := r.need.DismissKeys()
			return m, m.do(func() (string, error) { return "dismissed", m.core.Store.DismissQueue(keys...) }), true
		}
		return m, nil, false
	}
	if r.need != nil && r.need.Steer != nil && key == "x" {
		id := r.need.Key()
		return m, m.do(func() (string, error) { return "dismissed", m.core.Store.DismissQueue(id) }), true
	}
	if r.need != nil && r.need.Goal != nil && key == "x" {
		id := r.need.Goal.ID
		return m, m.do(func() (string, error) { return id + " cleared", m.core.AckGoal(id) }), true
	}
	if r.need != nil && r.item != nil && len(r.need.Asks) > 1 && key == "enter" {
		if m.unfolded == nil {
			m.unfolded = map[string]bool{}
		}
		m.unfolded[r.need.Key()] = !m.unfolded[r.need.Key()]
		return m, nil, true
	}
	n, ok := m.nodeOf(r)
	if !ok {
		return m, nil, false
	}
	switch key {
	case "enter":
		if r.node == nil && (r.need == nil || (r.need.Goal == nil && r.need.Steer == nil)) {
			return m, nil, false
		}
		if n.Goal != nil {
			m.push(modeGoal)
			m.goal = n.Manager.ID
			m.feedFrom = m.board.At
			if err := m.core.Looked(n.Goal.ID); err != nil {
				m.message = "error: " + err.Error()
			}
		} else {
			m.push(modeChief)
			m.chief = n.Manager.ID
		}
		return m, nil, true
	case "space":
		if r.node == nil {
			return m, nil, false
		}
		if m.picked == nil {
			m.picked = map[string]bool{}
		}
		if m.picked[n.Manager.ID] {
			delete(m.picked, n.Manager.ID)
		} else {
			m.picked[n.Manager.ID] = true
		}
		return m, nil, true
	case "B":
		if host, rest, ok := core.SplitRemote(n.Manager.ID); ok && n.Manager.Host != "" {
			head := " " + pal.bold.Render(m.tagged(host, n.Manager.Label())) + pal.dim.Render("  "+briefOf(n)) + "\n\n"
			m.show("B", head+pal.dim.Render("reading it from "+m.hostTag(host)+"…"))
			c := m.core
			return m, func() tea.Msg {
				out, err := c.RunRemote(host, "goal", "show", rest)
				return screenMsg{head + out, err}
			}, true
		}
		text, err := os.ReadFile(briefOf(n))
		if err != nil {
			m.message = "error: " + err.Error()
			return m, nil, true
		}
		m.show("B", " "+pal.bold.Render(n.Manager.Label())+pal.dim.Render("  "+briefOf(n))+"\n\n"+string(text))
		return m, nil, true
	case "W":
		url, open := core.BriefURL(n.Manager.ID), m.openURL
		return m, m.do(func() (string, error) { return "opened " + url, open(url) }), true
	case "S":
		return m, m.steerPlan(n.Manager.ID), true
	case "V":
		url, ok := m.artifact(n)
		if !ok {
			m.message = n.Manager.ID + " has registered no artifact; B shows its brief"
			return m, nil, true
		}
		open := m.openURL
		return m, m.do(func() (string, error) { return "opened " + url, open(url) }), true
	case "o", "O":
		if r.session != nil {
			return m, nil, false
		}
		if n.Live == nil {
			m.message = n.Manager.ID + " is not running; R relaunches it"
			return m, nil, true
		}
		m.beside = key == "O"
		next, cmd := m.open(row{session: n.Live})
		return next.(Model), cmd, true
	case "s":
		if r.session != nil && r.node == nil {
			return m, nil, false
		}
		targets := m.steerTargets(r)
		m.steer, m.subtree = nil, false
		var names []string
		for _, t := range targets {
			m.steer = append(m.steer, t.ID)
			names = append(names, t.ID)
		}
		m.startInput(inSteer, "", "", "", false)
		m.inLabel = strings.Join(names, ", ")
		return m, nil, true
	case "R":
		id, board := n.Manager.ID, m.board
		m.confirm("relaunch "+n.Manager.Label()+"? its session ends and a fresh one starts from its prompt and brief", m.do(func() (string, error) {
			mg, err := m.core.ReplaceManager(board, id)
			return mg.ID + " relaunched as " + core.Short(mg.Session), err
		}))
		return m, nil, true
	case "K":
		if r.node == nil {
			return m, nil, false
		}
		id, label := n.Manager.ID, n.Manager.Label()
		what := "drop goal " + label + "? ym stops supervising it; its sessions keep running"
		if n.Goal == nil {
			what = "retire " + label + "? its reports report to its own chief or you"
		}
		m.confirm(what, m.do(func() (string, error) {
			_, err := m.core.DropGoal(id)
			return "dropped " + id, err
		}))
		return m, nil, true
	case "c":
		if r.node == nil {
			return m, nil, false
		}
		m.inPicks = []string{n.Manager.ID}
		for id := range m.picked {
			if id != n.Manager.ID {
				m.inPicks = append(m.inPicks, id)
			}
		}
		m.startInput(inAttach, "", "", n.Manager.ParentID, false)
		m.inLabel = strings.Join(m.inPicks, ", ")
		return m, nil, true
	}
	return m, nil, false
}

// steerPlan opens the owner's editor on the plan of the goal or chief id and records what they save as its next revision.
func (m Model) steerPlan(id string) tea.Cmd {
	mg, was, rev, err := m.core.Context(id)
	if err != nil {
		return m.do(func() (string, error) { return "", err })
	}
	f, err := os.CreateTemp("", "ym-steer-"+mg.ID+"-*.md")
	if err != nil {
		return m.do(func() (string, error) { return "", err })
	}
	path := f.Name()
	f.Close()
	if err := core.EditPlan(path, mg, was, rev); err != nil {
		return m.do(func() (string, error) { return "", err })
	}
	c := m.core
	return tea.ExecProcess(core.EditorCommand(path), func(err error) tea.Msg {
		defer os.Remove(path)
		if err != nil {
			return doneMsg{"", fmt.Errorf("editor: %w", err)}
		}
		text, err := core.ReadPlan(path, was)
		if err != nil || text == "" {
			return doneMsg{fmt.Sprintf("%s unchanged at revision %d", mg.ID, rev), err}
		}
		b, err := c.Board()
		if err != nil {
			return doneMsg{"", err}
		}
		st, err := c.SteerContext(b, mg.ID, text, core.Owner)
		return doneMsg{fmt.Sprintf("%s steered to revision %d; it shows unadopted until its manager adopts it", mg.ID, st.Revision), err}
	})
}

// goalInput runs what a goal, chief, steer or attach input line asked for.
func (m Model) goalInput(text string) (tea.Model, tea.Cmd) {
	if (m.inKind == inGoal || m.inKind == inChief) && text != "" && m.launchOn != "" {
		return m.launchRemote(text)
	}
	switch m.inKind {
	case inGoal:
		if text == "" {
			return m, nil
		}
		repo := m.newL.Repo
		return m, m.do(func() (string, error) {
			g, mg, err := m.core.NewGoal(text, repo, "")
			if err == nil {
				err = m.core.Store.SetLaunchTarget("")
			}
			if err == nil && mg.LastError != "" {
				return g.ID + " recorded; its manager did not start: " + mg.LastError, nil
			}
			return g.ID + " started; " + mg.ID + " owns it", err
		})
	case inChief:
		if text == "" {
			return m, nil
		}
		repo, over := m.newL.Repo, m.inPicks
		m.picked = nil
		return m, m.do(func() (string, error) {
			c, err := m.core.NewChief(text, "", repo, over)
			if err == nil {
				err = m.core.Store.SetLaunchTarget("")
			}
			return "chief " + c.ID + " over " + plural(len(over), "report"), err
		})
	case inAttach:
		ids := m.inPicks
		m.picked = nil
		return m, m.do(func() (string, error) {
			for _, id := range ids {
				if err := m.core.Attach(id, text); err != nil {
					return "", err
				}
			}
			if text == "" {
				return strings.Join(ids, ", ") + " report to you", nil
			}
			return strings.Join(ids, ", ") + " report to " + text, nil
		})
	case inSteer:
		if text == "" {
			return m, nil
		}
		ids, subtree, board := m.steer, m.subtree, m.board
		m.picked = nil
		return m, m.do(func() (string, error) {
			n, err := m.core.Steer(board, ids, subtree, true, text)
			return fmt.Sprintf("sent to %s", plural(n, "manager")), err
		})
	}
	return m, nil
}

// steerHasChief reports whether the steer input reaches a chief, so tab can widen it to the subtree.
func (m Model) steerHasChief() bool {
	for _, id := range m.steer {
		if n, ok := m.board.Node(id); ok && n.Goal == nil {
			return true
		}
	}
	return false
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func (m Model) pickLabel() string {
	if m.forGoal {
		return "new goal, repository"
	}
	return "new " + m.newL.Harness + " session, repository"
}

// askBlock is the end of a waiting reply as the agent wrote it: its last block of non-empty lines, at most a dozen.
func askBlock(reply string) []string {
	lines := strings.Split(strings.TrimRight(strings.ReplaceAll(reply, "\t", "    "), "\n "), "\n")
	start := len(lines)
	for start > 0 && len(lines)-start < 12 {
		if strings.TrimSpace(lines[start-1]) == "" && len(lines)-start >= 3 {
			break
		}
		start--
	}
	var out []string
	for _, l := range lines[start:] {
		out = append(out, strings.TrimRight(strings.ReplaceAll(l, "\r", ""), " "))
	}
	return out
}
