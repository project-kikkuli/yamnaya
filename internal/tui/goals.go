package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
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

// treeRows are the goal rows of the board grouped by repository: each goal reporting to the owner under its repository and
// every chief with its reports nested under it in the chiefs group, or when flat every goal under its own repository.
func (m Model) treeRows() []row {
	var out []row
	if m.flat {
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
			if m.matches(all[i].Goal.Ask, all[i].Manager.Name, m.hostText(all[i].Manager.Host)) {
				out = append(out, row{node: &all[i], group: goalGroup(all[i])})
			}
		}
		return out
	}
	tops := make([]*core.Node, len(m.board.Tree))
	for i := range m.board.Tree {
		tops[i] = &m.board.Tree[i]
	}
	sort.SliceStable(tops, func(i, j int) bool { return groupLess(goalGroup(*tops[i]), goalGroup(*tops[j])) })
	for _, n := range tops {
		out = append(out, m.sceneNodeRows([]core.Node{*n}, goalGroup(*n), 0)...)
	}
	return out
}

// chiefGroup is the goal group of chiefs and everything they oversee.
const chiefGroup = "chiefs"

// goalGroup is the group a top-level goal or chief row shows in: the goal's repository, or the chiefs.
func goalGroup(n core.Node) string {
	if n.Goal == nil {
		return chiefGroup
	}
	return "repo " + n.Repo
}

// groupLess puts the chiefs first, then repositories by name.
func groupLess(a, b string) bool {
	if a == chiefGroup || b == chiefGroup {
		return a == chiefGroup && b != chiefGroup
	}
	if la, lb := groupLabel(a), groupLabel(b); la != lb {
		return la < lb
	}
	return a < b
}

// groupLabel is a goal group's heading.
func groupLabel(group string) string {
	repo, ok := strings.CutPrefix(group, "repo ")
	if !ok {
		return group
	}
	if label := core.RepoLabel(repo); label != "" {
		return label
	}
	return "no repository"
}

// treeMatches reports whether the filter matches any node in ns or below them.
func (m Model) treeMatches(ns []core.Node) bool {
	for _, n := range ns {
		if m.matches(nodeText(n), m.hostText(n.Manager.Host)) || m.treeMatches(n.Children) {
			return true
		}
	}
	return false
}

// minAsk is how much of a goal row its ask keeps before its numbers give way; a chief row's remit gives way first.
const (
	minAsk = 56
	// minNames and minRow are the least a goal or chief row's first column shows, and the least it leaves its vibe and text.
	minNames = 12
	minRow   = 24
	minRemit = 10
)

// nodeStats are a goal's stats, or a chief's own plus the sum over every report below it.
func nodeStats(n core.Node) core.Stats {
	out := n.Stats
	for _, c := range n.Children {
		st := nodeStats(c)
		out.Running += st.Running
		out.Idle += st.Idle
		out.PRs = out.PRs.Plus(st.PRs)
		if st.Spend != nil {
			sp := *st.Spend
			if out.Spend != nil {
				sp = out.Spend.Plus(sp)
			}
			out.Spend = &sp
		}
		if st.Vibe != nil && (out.Vibe == nil || st.Vibe.At.After(out.Vibe.At)) {
			out.Vibe = st.Vibe
		}
		if st.ProgressAt.After(out.ProgressAt) {
			out.Progress, out.ProgressAt = st.Progress, st.ProgressAt
		}
	}
	return out
}

// statCells are a goal or chief row's numbers: its vibe always, then as many as fit in room of agents running and idle, pull
// requests open, green and merged with CI, spend and how long it has run, the last ones dropped first.
func (m Model) statCells(st core.Stats, room int) string {
	agents := pad("", 9)
	if st.Running+st.Idle > 0 {
		agents = pad(fmt.Sprintf("%d▶ %d idle", st.Running, st.Idle), 9)
		if st.Running > 0 {
			agents = pal.green.Render(agents)
		}
	}
	prs := pal.dim.Render(pad("no PRs", 11))
	if t := st.PRs; t.Open+t.Merged > 0 {
		prs = pad(fmt.Sprintf("%dPR %d✓ %dm", t.Open, t.Green, t.Merged), 11)
	}
	ci := pad("", 4)
	switch st.PRs.CI() {
	case "failing":
		ci = pal.red.Render(pad("CI✗", 4))
	case "running":
		ci = pal.yellow.Render(pad("CI…", 4))
	case "green":
		ci = pal.green.Render(pad("CI✓", 4))
	}
	spend := pal.dim.Render(pad("$?", 8))
	if st.Spend != nil {
		spend = pad(st.Spend.Dollars(), 8)
	}
	up := pad("", 5)
	if !st.Started.IsZero() {
		up = pal.dim.Render(pad(core.Span(m.board.At.Sub(st.Started)), 5))
	}
	vibe := pal.dim.Render(pad("no vibe", 12))
	if v := st.Vibe; v != nil {
		vibe = pad(ansi.Truncate(v.Place, 12, "…"), 12)
		if m.board.At.Sub(v.At) > core.VibeStale {
			vibe = pal.dim.Render(vibe)
		}
	}
	shown := []string{vibe}
	used := ansi.StringWidth(vibe) + 1
	for _, c := range []string{agents, prs, ci, spend, up} {
		if used += ansi.StringWidth(c) + 1; used > room {
			break
		}
		shown = append(shown, c)
	}
	return strings.Join(shown, " ") + " "
}

func nodeText(n core.Node) string {
	if n.Goal != nil {
		return n.Goal.Ask + " " + n.Manager.Name
	}
	return n.Manager.Name
}

// nodePath lists the node and its ancestors, nearest first, in the current hierarchy.
func nodePath(ns []core.Node, id string) []core.Node {
	for _, n := range ns {
		if n.Manager.ID == id {
			return []core.Node{n}
		}
		if path := nodePath(n.Children, id); len(path) > 0 {
			return append(path, n)
		}
	}
	return nil
}

var standStyle = map[string]func(string) string{
	core.StandDone:      func(s string) string { return pal.green.Render(s) },
	core.StandKilled:    func(s string) string { return pal.dim.Render(s) },
	core.StandVerifying: func(s string) string { return pal.yellow.Render(s) },
	core.StandMoving:    func(s string) string { return s },
	core.StandQuiet:     func(s string) string { return pal.dim.Render(s) },
	core.StandAdrift:    func(s string) string { return pal.red.Render(s) },
	core.StandDown:      func(s string) string { return pal.red.Render(s) },
	core.StandStuck:     func(s string) string { return pal.red.Render(s) },
	core.StandOff:       func(s string) string { return pal.yellow.Render(s) },
}

// nodeLines is a goal or chief row, indented under its chief: who owns it, how it stands, how fresh its brief is, and what it is about.
func (m Model) nodeLines(i int, n core.Node, depth int) string {
	mark := m.mark(i)
	if m.picked[n.Manager.ID] && !m.dyingAt[i] {
		mark = mark[:len(mark)-1] + "*"
	}
	stand := standStyle[n.Standing](pad(n.Standing, 7))
	var head, what string
	host := n.Manager.Host
	keep := minAsk
	names := m.nameColumn(depth)
	if n.Goal != nil {
		head = pad(ansi.Truncate(n.Goal.Shown(), names, "…"), names)
		what = m.tagged(host, n.Goal.Ask)
	} else {
		head = pal.bold.Render(pad(ansi.Truncate(n.Manager.Shown(), names, "…"), names))
		what = m.tagged(host, pal.dim.Render(plural(n.Count(), "goal"))+remit(n))
		keep = minRemit
	}
	st := nodeStats(n)
	switch {
	case st.Progress != "":
		what += pal.dim.Render("  " + oneLine(st.Progress) + " · " + age(m.board.At, st.ProgressAt))
	case n.Feed.Latest != nil:
		what += pal.dim.Render("  " + n.Feed.Latest.Payload)
	}
	indent := strings.Repeat("  ", depth)
	line := mark + indent + head + " " + stand + " " + briefTag(m.board.At, n) + " "
	news := ""
	if u := n.Unread(); u > 0 {
		news = pal.yellow.Render(fmt.Sprintf("%d new ", u))
	}
	tags := planTags(n)
	line += m.statCells(st, m.width-ansi.StringWidth(line+news+tags)-keep) + news + tags
	b := line + ansi.Truncate(oneLine(what), max(10, m.width-ansi.StringWidth(line)-1), "…") + "\n"
	if i != m.cursor {
		return b
	}
	var meta []string
	if n.Goal != nil {
		meta = append(meta, "moved "+age(m.board.At, n.Goal.LastMovedAt))
		if n.Goal.State == core.GoalDone && n.Goal.Summary != "" {
			meta = append(meta, "done: "+n.Goal.Summary)
		}
		if n.Goal.Evidence != "" {
			meta = append(meta, n.Goal.Evidence)
		}
	} else {
		meta = append(meta, n.Manager.Shown())
	}
	if n.Live != nil && n.Live.Context > 0 {
		meta = append(meta, "context "+core.Kilo(n.Live.Context))
	}
	if n.Manager.LastError != "" {
		meta = append(meta, "launch: "+n.Manager.LastError)
	}
	if n.Goal == nil || !n.Goal.Ended() {
		if f := n.Fresh; f.Stale() {
			meta = append(meta, pal.yellow.Render(briefMeta(m.board.At, n)))
		} else {
			meta = append(meta, briefMeta(m.board.At, n))
		}
	}
	if state := m.hostState(host); state != "" {
		meta = append(meta, m.hostTag(host)+" "+state)
	}
	keys := "enter open · r reply · b brief · B login · c chief · S steer plan · R relaunch · W brief page · o session · space select"
	if len(n.Children)+len(n.Agents) > 0 || n.Live != nil {
		keys += " · ← collapse · → expand"
	}
	switch {
	case n.Goal != nil && n.Goal.Ended():
		keys += " · x clear · d close"
	case n.Goal != nil:
		keys += " · K kill · d close"
	default:
		keys += " · K retire"
	}
	if brief, ok := m.artifact(n); ok && brief != "" {
		keys += " · V artifact"
	}
	under := "         " + indent
	b += pal.dim.Render(under+strings.Join(meta, " | ")) + "\n"
	for _, l := range wrapHints(under, keys, m.width) {
		b += pal.dim.Render(l) + "\n"
	}
	return b
}

// nameColumn is the width of a goal or chief row's first column: the longest name on the board, as far as the row leaves room.
func (m Model) nameColumn(depth int) int {
	longest := 0
	var walk func([]core.Node)
	walk = func(ns []core.Node) {
		for i := range ns {
			name := ns[i].Manager.Shown()
			if ns[i].Goal != nil {
				name = ns[i].Goal.Shown()
			}
			longest = max(longest, ansi.StringWidth(name))
			walk(ns[i].Children)
		}
	}
	walk(m.board.Tree)
	const rest = 2 + 1 + 7 + 1 + 11 + 1 + minRow
	return max(minNames, min(longest, m.width-rest-2*depth))
}

// remit is what a chief is about: its latest steer, else its brief's assessment.
func remit(n core.Node) string {
	switch {
	case n.Steer != nil:
		return "  " + oneLine(n.Steer.Text)
	case n.Fresh.Brief != nil && n.Fresh.Brief.Assessment != "":
		return "  " + oneLine(n.Fresh.Brief.Assessment)
	}
	return ""
}

// briefTag is how fresh a row's brief is at a glance: its age, stale, or none yet.
func briefTag(at time.Time, n core.Node) string {
	f := n.Fresh
	switch {
	case f.Brief == nil:
		return pal.dim.Render(pad("no brief", 11))
	case f.Stale() && (n.Goal == nil || !n.Goal.Ended()):
		return pal.yellow.Render(pad("brief stale", 11))
	}
	a := strings.TrimSuffix(age(at, f.Brief.CreatedAt), " ago")
	if a == "just now" {
		a = "now"
	}
	return pad("brief "+a, 11)
}

// planTags flag a steer the manager has not adopted, so it shows at a glance.
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

func (m Model) planField(n core.Node) string {
	st := n.Steer
	if st == nil {
		return ""
	}
	state := "adopted " + st.AdoptedAt.Local().Format(time.Kitchen)
	if !st.Adopted() {
		state = pal.yellow.Render("not adopted")
	}
	return fmt.Sprintf("r%d by %s, %s: %s", st.Revision, m.board.Org.SteerBy(*st), state, oneLine(st.Text))
}

func (m Model) artifact(n core.Node) (string, bool) {
	if n.Goal != nil && n.Goal.ArtifactURL != "" {
		return n.Goal.ArtifactURL, true
	}
	return n.Manager.ArtifactURL, n.Manager.ArtifactURL != ""
}

// goalToClear is the goal x clears on r: a stuck goal's queue row, or a done goal's row on the goal tree.
func goalToClear(r row) *core.Goal {
	switch {
	case r.need != nil && r.need.Goal != nil:
		return r.need.Goal
	case r.node != nil && r.node.Goal != nil && r.node.Goal.Ended():
		return r.node.Goal
	}
	return nil
}

// goalName is the goal's name, tagged with its host's short name when it runs on another host.
func (m Model) goalName(g core.Goal) string {
	if g.Name != "" {
		return m.tagged(g.Host, g.Name)
	}
	_, id, _ := core.SplitRemote(g.ID)
	return m.tagged(g.Host, id)
}

func briefOf(n core.Node) string {
	if n.Goal != nil {
		return n.Goal.BriefPath
	}
	return n.Manager.BriefPath
}

// queueNeeds are the ranked rows on screen, without the later fold.
func (m Model) queueNeeds(rows []row) []core.Need {
	var out []core.Need
	for _, r := range rows {
		if r.need != nil && !m.isLater(r.need) {
			out = append(out, *r.need)
		}
	}
	return out
}

// queueLabels numbers the ranked rows; a later row has no rank.
func (m Model) queueLabels(rows []row) map[*core.Need]string {
	ranks := core.RankLabels(len(m.queueNeeds(rows)))
	out := map[*core.Need]string{}
	at := 0
	for _, r := range rows {
		if r.need == nil {
			continue
		}
		if m.isLater(r.need) {
			out[r.need] = core.LaterLabel
			continue
		}
		out[r.need] = ranks[at]
		at++
	}
	return out
}

// queueKeys are the keys of the selected row.
func queueKeys(n core.Need, later bool) string {
	var keys []string
	switch n.Accept() {
	case core.CheckGo:
		keys = append(keys, "a go ahead")
	case core.CheckAccept:
		keys = append(keys, "a accept rec")
	case core.CheckMerge:
		keys = append(keys, "a merge")
	}
	switch {
	case n.Item != nil:
		keys = append(keys, "⏎ full", "r reply", "o session")
	case n.Session != nil:
		keys = append(keys, "⏎ go to session", "r reply")
	default:
		keys = append(keys, "⏎ open")
	}
	if later && (n.Item == nil || n.Item.State == core.ItemSnoozed) {
		keys = append(keys, "u back")
	} else {
		keys = append(keys, "l later")
	}
	if n.Session == nil {
		keys = append(keys, "x dismiss")
	}
	return strings.Join(append(keys, "f , . rank"), " · ")
}

// queueRowLines draws one queue row with the renderer the brief page shares; the selected row opens under itself, and no other.
func (m Model) queueRowLines(i int, n core.Need, label string, terminal int) string {
	line, side := core.RowLineIn(n, label, terminal, m.width, m.board.At)
	later := label == core.LaterLabel
	style := pal.plain
	if later {
		style = pal.dim
	}
	sel := i == m.cursor
	switch {
	case m.dyingAt[i]:
		style = pal.red
	case sel:
		style = style.Reverse(true)
	}
	tag := m.hostTag(m.needHost(n))
	if side != "" && tag != "" {
		side = ansi.Truncate(" "+tag+side, max(1, m.width-ansi.StringWidth(line)), "…")
	}
	b := style.Render(line) + pal.dim.Render(side) + "\n"
	if sel {
		if tag != "" {
			b += pal.dim.Render("  "+tag) + "\n"
		}
		for _, l := range core.RowDetail(n, m.width, m.board.At) {
			b += pal.dim.Render("  "+l) + "\n"
		}
		if n.Item != nil {
			if line := m.unprepared(*n.Item); line != "" {
				b += pal.yellow.Render("  "+ansi.Truncate(line, max(10, m.width-2), "…")) + "\n"
			}
			if line := evidenceLine(m.board.Org, *n.Item); line != "" {
				b += pal.dim.Render("  "+ansi.Truncate(line, max(10, m.width-2), "…")) + "\n"
			}
		}
		if n.Goal != nil && n.Goal.Evidence != "" {
			b += pal.dim.Render("  "+ansi.Truncate(oneLine(n.Goal.Evidence), max(10, m.width-2), "…")) + "\n"
		}
		b += pal.dim.Render(ansi.Wordwrap("  "+queueKeys(n, later), max(10, m.width), " ")) + "\n"
	}
	return b
}

func (m Model) goalView() viewPage {
	n, ok := m.board.Node(m.goal)
	if !ok {
		return textPage(" that goal left the board\n", "")
	}
	b := viewPage{}
	g := n.Goal
	b.WriteString(fmt.Sprintf(" %s  %s  %s  %s\n", pal.bold.Render("GOAL "+g.Shown()), standStyle[n.Standing](n.Standing), filepath.Base(g.Repo), pal.dim.Render("moved "+age(m.board.At, g.LastMovedAt))))
	b.WriteString(" " + ansi.Wrap(g.Ask, max(20, m.width-2), " ") + "\n")
	m.field(&b, "manager", managerState(m.board, n))
	m.field(&b, "plan", m.planField(n))
	m.field(&b, "brief", briefMeta(m.board.At, n)+"  "+core.BriefURL(n.Manager.Ref())+pal.dim.Render("  b shows it, W opens the page"))
	if url, ok := m.artifact(n); ok {
		m.field(&b, "artifact", url+pal.dim.Render("  V opens it"))
	}
	if g.Evidence != "" {
		m.field(&b, "stuck", g.Evidence)
	}
	if g.Summary != "" {
		m.field(&b, "done", g.Summary)
	}
	b.WriteString(m.treePage().String())
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
	b.WriteString(fmt.Sprintf(" %s  %s\n", pal.bold.Render("CHIEF "+n.Manager.Shown()), standStyle[n.Standing](n.Standing)))
	m.field(&b, "session", managerState(m.board, n))
	m.field(&b, "plan", m.planField(n))
	m.field(&b, "brief", briefMeta(m.board.At, n)+"  "+core.BriefURL(n.Manager.Ref())+pal.dim.Render("  b shows it, W opens the page with its reports' briefs"))
	b.WriteString(m.treePage().String())
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
	if r.header != "" && key == "enter" {
		m.treeMove("right")
		return m, nil, true
	}
	switch key {
	case "left", "right":
		m.treeMove(key)
		return m, nil, true
	case "n", "N":
		if key == "N" && r.node != nil && r.session == nil {
			m.startInput(inName, r.node.Manager.ID, "", r.node.Manager.Shown(), true)
			m.inLabel, m.pickErr = r.node.Manager.Shown(), ""
			return m, nil, true
		}
		if m.core.Launches == nil {
			m.message = "ym's launcher is unavailable"
			return m, nil, true
		}
		m.newL, m.forGoal = core.NewLaunch{}, key == "n"
		m.loadTarget()
		m.loadHarness("goal")
		if n, ok := m.nodeOf(r); ok && n.Manager.Role == core.RoleChief && n.Manager.Repo == "" {
			m.forGoal, m.launchOn = true, n.Manager.Host
		}
		if m.forGoal {
			m.openRepoPicker()
			return m, m.scanRepos(), true
		}
		m.newL.Repo = core.MainCheckout(m.repoOf(r))
		m.targetRepo()
		m.startInput(inGoal, "", "", "", false)
		return m, nil, true
	case "+":
		if m.core.Launches == nil {
			m.message = "ym's launcher is unavailable"
			return m, nil, true
		}
		m.forGoal = false
		m.newL = core.NewLaunch{Repo: core.MainCheckout(m.repoOf(r))}
		m.loadTarget()
		m.targetRepo()
		m.loadHarness("session")
		if n, ok := m.nodeOf(r); ok && n.Manager.Role == core.RoleChief && n.Manager.Repo == "" {
			m.newL.Repo, m.launchOn = "", n.Manager.Host
			m.openRepoPicker()
			return m, m.scanRepos(), true
		}
		m.startInput(inNew, "", "", "", false)
		return m, nil, true
	case "t":
		if m.mode == modeList {
			m.flat = !m.flat
			m.follow(rowKey(r), nil, nil)
			return m, nil, true
		}
	case "C":
		m.newChief(r)
		return m, nil, true
	case "c":
		if m.mode == modeList || m.mode == modeChief || m.mode == modeGoal {
			m.openChiefMenu(r)
			return m, nil, true
		}
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
	if r.need != nil && r.need.Relay != nil && key == "x" {
		seq := r.need.Relay.Seq
		return m, m.do(func() (string, error) {
			_, err := m.core.Store.DropRelay(seq)
			return fmt.Sprintf("relay %d dropped", seq), err
		}), true
	}
	if r.need != nil && r.need.Steer != nil && key == "x" {
		id := r.need.Key()
		return m, m.do(func() (string, error) { return "dismissed", m.core.Store.DismissQueue(id) }), true
	}
	if g := goalToClear(r); g != nil && key == "x" {
		id, name := g.ID, g.Shown()
		return m, m.do(func() (string, error) { return name + " cleared", m.core.AckGoal(id) }), true
	}
	if r.need != nil && r.item != nil && len(r.need.Asks) > 1 && key == "enter" {
		m.setFold(r, !r.closed)
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
			m.firstContent()
			m.feedFrom = m.board.At
			if host, rest, ok := core.SplitRemote(n.Goal.ID); ok && n.Goal.Host != "" {
				c := m.core
				return m, m.do(func() (string, error) {
					_, err := c.RunOrRelay(host, "goal", "feed", rest)
					return "", err
				}), true
			}
			if err := m.core.Looked(n.Goal.ID); err != nil {
				m.message = "error: " + err.Error()
			}
		} else {
			m.push(modeChief)
			m.chief = n.Manager.ID
			m.firstContent()
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
	case "b":
		if host, rest, ok := core.SplitRemote(n.Manager.ID); ok && n.Manager.Host != "" {
			head := " " + pal.bold.Render(m.tagged(host, n.Manager.Label())) + pal.dim.Render("  "+briefOf(n)) + "\n\n"
			if f := n.Fresh.Brief; f != nil {
				m.show("B", head+pal.dim.Render(briefMeta(m.board.At, n)+", as "+m.hostTag(host)+" last told this board")+"\n\n"+f.Markdown(n.Manager.Label()))
				m.screenCopy = f.Markdown(n.Manager.Label())
				return m, nil, true
			}
			if err := m.silent(host); err != nil {
				m.show("B", head+pal.red.Render("cannot read it: "+err.Error()))
				return m, nil, true
			}
			m.show("B", head+pal.dim.Render("reading it from "+m.hostTag(host)+"…"))
			c := m.core
			return m, func() tea.Msg {
				out, err := c.RunRemote(host, "goal", "show", rest)
				return screenMsg{head + out, err}
			}, true
		}
		text, err := m.core.BriefPage(n.Manager.ID)
		if err != nil {
			m.message = "error: " + err.Error()
			return m, nil, true
		}
		m.show("B", " "+pal.bold.Render(n.Manager.Label())+pal.dim.Render("  "+briefOf(n))+"\n\n"+text)
		m.screenCopy = text
		return m, nil, true
	case "W":
		url, open := core.BriefURL(n.Manager.Ref()), m.openURL
		return m, m.do(func() (string, error) { return "opened " + url, open(url) }), true
	case "S":
		return m, m.steerPlan(n.Manager.ID), true
	case "V":
		url, ok := m.artifact(n)
		if !ok {
			m.message = n.Manager.Shown() + " has registered no artifact; b shows its brief"
			return m, nil, true
		}
		open := m.openURL
		return m, m.do(func() (string, error) { return "opened " + url, open(url) }), true
	case "o", "O":
		if r.session != nil {
			return m, nil, false
		}
		if n.Live == nil {
			m.message = n.Manager.Shown() + " is not running; R relaunches it"
			return m, nil, true
		}
		m.beside = key == "O"
		next, cmd := m.open(row{session: n.Live})
		return next.(Model), cmd, true
	case "s", "r":
		if r.session != nil && r.node == nil {
			return m, nil, false
		}
		targets := m.steerTargets(r)
		m.steer, m.subtree = nil, false
		var names []string
		for _, t := range targets {
			m.steer = append(m.steer, t.ID)
			names = append(names, t.Shown())
		}
		m.startInput(inSteer, "", "", "", false)
		m.inLabel = strings.Join(names, ", ")
		return m, nil, true
	case "R":
		id, name, board := n.Manager.ID, n.Manager.Shown(), m.board
		m.confirm("relaunch "+n.Manager.Label()+"? its session ends and a fresh one starts from its prompt and brief", m.do(func() (string, error) {
			mg, err := m.core.ReplaceManager(board, id)
			if mg.Host != "" {
				return name + " relaunched on " + m.hostTag(mg.Host), err
			}
			return name + " relaunched as " + core.Short(mg.Session), err
		}))
		return m, nil, true
	case "K":
		if r.node == nil {
			return m, nil, false
		}
		if (n.Goal != nil && n.Goal.Ended()) || m.killing["node:"+n.Manager.ID] {
			return m, nil, true
		}
		m.message = "counting what killing " + n.Manager.Label() + " ends"
		return m, m.weighGoal(n), true
	case "X":
		if n.Goal == nil || (m.mode != modeGoal && r.node == nil) {
			return m, nil, false
		}
		m.dropPR(n)
		return m, nil, true
	case "d":
		if n.Goal == nil || (m.mode != modeGoal && r.node == nil) {
			return m, nil, false
		}
		m.closeGoal(n)
		return m, nil, true
	case "D":
		if r.node == nil || n.Manager.ParentID == "" {
			return m, nil, false
		}
		m.detach(n)
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
			return doneMsg{fmt.Sprintf("%s unchanged at revision %d", mg.Shown(), rev), err}
		}
		b, err := c.Board()
		if err != nil {
			return doneMsg{"", err}
		}
		st, err := c.SteerContext(b, mg.ID, text, core.Owner)
		if st.Sent {
			return doneMsg{"new plan for " + mg.Ref() + " sent to its host; the result comes back to you", err}
		}
		return doneMsg{fmt.Sprintf("%s steered to revision %d; it shows unadopted until its manager adopts it", mg.Shown(), st.Revision), err}
	})
}

// goalInput runs what a goal, chief name, chief remit or steer input line asked for.
func (m Model) goalInput(text string) (tea.Model, tea.Cmd) {
	if m.inKind == inGoal && text != "" && m.launchOn != "" {
		return m.launchRemote(text)
	}
	switch m.inKind {
	case inGoal:
		if text == "" {
			return m, nil
		}
		repo, harness := m.newL.Repo, m.newL.Harness
		return m, m.do(func() (string, error) {
			g, mg, err := m.core.NewGoal(text, repo, harness, m.newL.Profile)
			if err == nil && g.Host != "" {
				return "started on " + g.Host + ": " + strings.SplitN(g.Evidence, "\n", 2)[0], m.core.Store.SetLaunchTarget(g.Host)
			}
			if err == nil {
				err = m.launchedHere("goal", harness)
			}
			if err == nil && mg.LastError != "" {
				return g.Shown() + " recorded; its manager did not start: " + mg.LastError, nil
			}
			return g.Shown() + " started", err
		})
	case inChief:
		if text == "" {
			return m, nil
		}
		m.chiefName = text
		m.startInput(inRemit, "", "", "", false)
		return m, nil
	case inRemit:
		if m.launchOn != "" {
			return m.launchRemote(text)
		}
		name, repo, harness, over := m.chiefName, m.newL.Repo, m.newL.Harness, m.inPicks
		m.picked = nil
		return m, m.do(func() (string, error) {
			c, err := m.core.NewChief(name, text, repo, harness, over, m.newL.Profile)
			if err == nil && c.Host != "" {
				return "chief " + name + " started on " + c.Host, m.core.Store.SetLaunchTarget(c.Host)
			}
			if err == nil {
				err = m.launchedHere("chief", harness)
			}
			return "chief " + c.Shown() + " over " + plural(len(over), "report"), err
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
		return "new " + m.newL.Harness + " goal, repository"
	}
	return "new " + m.newL.Harness + " session, repository"
}

// shownRelay is what a relay carries as a person reads it: by the names the board has for the host it goes to.
func (m Model) shownRelay(r core.Relay) string {
	var y struct {
		Args []string `json:"args"`
	}
	if r.Kind == core.RelayYM && json.Unmarshal([]byte(r.Body), &y) == nil && len(y.Args) > 0 {
		return "ym " + strings.Join(m.board.Org.NameArgs(r.Dest, y.Args), " ")
	}
	return relayWhat(r)
}

// relayWhat names what a relay carries, as its owner would recognize it.
func relayWhat(r core.Relay) string {
	var y struct {
		Args []string `json:"args"`
	}
	if r.Kind == core.RelayYM && json.Unmarshal([]byte(r.Body), &y) == nil && len(y.Args) > 0 {
		return "ym " + strings.Join(y.Args, " ")
	}
	return "a " + r.Kind + " relay"
}

// closeGoal asks how the goal ended and why, then closes it as the owner: a chief's ym goal close, from the board.
func (m *Model) closeGoal(n core.Node) {
	goal, shown := n.Goal.ID, n.Goal.Ref()
	m.askLine("close "+shown+" as? 1 finished, 2 superseded, 3 abandoned, 4 finished past the verifier", "", false, func(m Model, pick string) (tea.Model, tea.Cmd) {
		order := core.CloseOrder{Ref: goal, Author: core.Owner}
		switch strings.TrimSpace(pick) {
		case "1", core.CloseFinished:
			order.As = core.CloseFinished
		case "2", core.CloseSuperseded:
			order.As = core.CloseSuperseded
		case "3", core.CloseAbandoned:
			order.As = core.CloseAbandoned
		case "4", core.CloseFinishedOverride:
			order.As, order.Override = core.CloseFinished, true
		case "":
			return m, nil
		default:
			m.message = "error: pick 1, 2, 3 or 4"
			return m, nil
		}
		m.askLine("why close "+shown+" as "+order.As, "", false, func(m Model, why string) (tea.Model, tea.Cmd) {
			if strings.TrimSpace(why) == "" {
				return m, nil
			}
			order.Why = why
			c := m.core
			return m, m.do(func() (string, error) {
				b, err := c.Board()
				if err != nil {
					return "", err
				}
				closed, err := c.CloseGoal(b, order)
				return closed.String(), err
			})
		})
		return m, nil
	})
}

// dropPR asks which of the goal's pull requests to untie, and why, then drops it on the host the goal runs on.
func (m *Model) dropPR(n core.Node) {
	prs := append(append([]string(nil), n.Feed.PRs...), n.Feed.Touched...)
	if len(prs) == 0 {
		m.message = n.Goal.Shown() + " has no pull requests tied to it"
		return
	}
	goal, shown := n.Goal.ID, n.Goal.Ref()
	why := func(url string) func(Model, string) (tea.Model, tea.Cmd) {
		return func(m Model, text string) (tea.Model, tea.Cmd) {
			m.askLine("why "+url+" is not "+shown+"'s", "", false, func(m Model, why string) (tea.Model, tea.Cmd) {
				if why == "" {
					return m, nil
				}
				c := m.core
				return m, m.do(func() (string, error) {
					if host, rest, ok := core.SplitRemote(goal); ok && n.Goal.Host != "" {
						_, err := c.RunOrRelay(host, "goal", "pr", "drop", rest, url, "--why", why)
						return "dropped " + url + " from " + shown, err
					}
					_, _, err := c.DropGoalPR("", goal, url, why)
					return "dropped " + url + " from " + shown, err
				})
			})
			return m, nil
		}
	}
	if len(prs) == 1 {
		next, _ := why(prs[0])(*m, "")
		*m = next.(Model)
		return
	}
	m.askLine(fmt.Sprintf("drop which pull request from %s? its number 1-%d, or its link", shown, len(prs)), "", false, func(m Model, text string) (tea.Model, tea.Cmd) {
		url := text
		if i, err := strconv.Atoi(text); err == nil && i >= 1 && i <= len(prs) {
			url = prs[i-1]
		}
		if !slices.Contains(prs, url) {
			m.message = text + " is not one of " + shown + "'s pull requests"
			return m, nil
		}
		return why(url)(m, "")
	})
}

func (m Model) goalSections(n core.Node) []treeNode {
	var out []treeNode
	b := viewPage{}
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

	out = append(out, textSection("node/"+n.Manager.ID+"/feed", "FEED", b.String()))
	if f := n.Feed; len(f.PRs)+len(f.Touched) > 0 {
		b = viewPage{}
		for i, url := range f.PRs {
			b.WriteString(fmt.Sprintf("   %d. %s\n", i+1, url))
		}
		for i, url := range f.Touched {
			b.WriteString(pal.dim.Render(fmt.Sprintf("   %d. %s  someone else's, context only", len(f.PRs)+i+1, url)) + "\n")
		}
		out = append(out, textSection("node/"+n.Manager.ID+"/prs", fmt.Sprintf("PULL REQUESTS (%d to land)", len(f.PRs)), b.String()))
	}

	b = viewPage{}
	var subs []core.Worker
	finished := 0
	for _, w := range n.Workers {
		switch {
		case w.Member == nil:
		case w.Active():
			subs = append(subs, w)
		default:
			finished++
		}
	}
	for _, w := range subs {
		line := "   " + pad(w.Name, 36) + " " + pal.state(w.State).Render(pad(w.State, 8)) + " " + oneLine(w.Activity) + pal.dim.Render(" "+age(m.board.At, w.At))
		b.WriteString(ansi.Truncate(line, max(20, m.width-1), "…") + "\n")
	}
	if len(subs) == 0 {
		b.WriteString(pal.dim.Render("   none running") + "\n")
	}
	if len(subs)+finished > 0 {
		out = append(out, textSection("node/"+n.Manager.ID+"/subagents", fmt.Sprintf("SUBAGENTS (%d running, %d done)", len(subs), finished), b.String()))
	}
	return out
}
