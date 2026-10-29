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
		out = append(out, m.nodeRows([]core.Node{*n}, goalGroup(*n), false)...)
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
	minAsk   = 56
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

// queueRows are the owner's queue, with every one of a session's folded asks listed under it while it is open.
func (m Model) queueRows() []row {
	var out []row
	for i := range m.board.Needs {
		n := &m.board.Needs[i]
		if !m.matches(m.projectName(n.Project), needText(*n), m.hostText(m.needHost(*n))) {
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
	all := false
	for _, ancestor := range nodePath(m.board.Tree, m.chief) {
		if m.matches(nodeText(ancestor), m.hostText(ancestor.Manager.Host)) {
			all = true
			break
		}
	}
	return m.nodeRows(n.Children, chiefGroup, all)
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

func expandable(n core.Node) bool { return n.Goal == nil && len(n.Children) > 0 }

func (m Model) folded(n core.Node) bool {
	return m.filter == "" && m.collapsed[n.Manager.ID] && expandable(n)
}

func (m Model) nodeRows(ns []core.Node, group string, all bool) []row {
	var out []row
	var walk func([]core.Node, int, bool)
	walk = func(ns []core.Node, depth int, all bool) {
		for i := range ns {
			n := &ns[i]
			hit := all || m.matches(nodeText(*n), m.hostText(n.Manager.Host))
			if hit || m.treeMatches(n.Children) {
				out = append(out, row{node: n, depth: depth, group: group})
				if !m.folded(*n) {
					walk(n.Children, depth+1, hit)
				}
			}
		}
	}
	walk(ns, 0, all)
	return out
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
	if n.Goal != nil {
		_, id, _ := core.SplitRemote(n.Goal.ID)
		head = pad(id, 8)
		what = m.tagged(host, n.Goal.Ask)
	} else {
		head = pal.bold.Render(pad(ansi.Truncate(n.Manager.Name, chiefName, "…"), chiefName))
		fold := ""
		if expandable(n) {
			fold = "▾ "
			if m.folded(n) {
				fold = "▸ "
			}
		}
		what = fold + m.tagged(host, pal.dim.Render(plural(n.Count(), "goal"))+remit(n))
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
		meta = append(meta, n.Manager.ID, "moved "+age(m.board.At, n.Goal.LastMovedAt))
		if n.Goal.State == core.GoalDone && n.Goal.Summary != "" {
			meta = append(meta, "done: "+n.Goal.Summary)
		}
		if n.Goal.Evidence != "" {
			meta = append(meta, n.Goal.Evidence)
		}
	} else {
		meta = append(meta, n.Manager.Name, n.Manager.ID)
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
	if expandable(n) {
		keys += " · ← collapse · → expand"
	}
	switch {
	case n.Goal != nil && n.Goal.Ended():
		keys += " · x clear"
	case n.Goal != nil:
		keys += " · K kill"
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

// chiefName is how much of a chief's name its row's first column holds.
const chiefName = 12

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

// goalName is the goal's id, tagged with its host's short name when it runs on another host.
func (m Model) goalName(g core.Goal) string {
	_, id, _ := core.SplitRemote(g.ID)
	return m.tagged(g.Host, id)
}

func briefOf(n core.Node) string {
	if n.Goal != nil {
		return n.Goal.BriefPath
	}
	return n.Manager.BriefPath
}

// managerName names a manager by its goal and ask, or a chief by its name, falling back to the id ym knows it by.
func (m Model) managerName(id string) string {
	n, ok := m.board.Node(id)
	switch {
	case !ok:
		return id
	case n.Goal != nil:
		return m.goalName(*n.Goal) + " (" + ansi.Truncate(oneLine(n.Goal.Ask), 40, "…") + ")"
	}
	return n.Manager.Label()
}

// queueLines draws the owner duties that are not an item or a session: stuck goals, steers, relays, merges, reviews and doc asks.
func (m Model) queueLines(i int, n core.Need) (string, bool) {
	sel := i == m.cursor
	var head, what, keys string
	switch {
	case n.Goal != nil:
		head, what = pal.red.Render(pad("stuck", 8)), m.goalName(*n.Goal)+" "+n.Goal.Ask
		keys = "enter open · r reply · b brief · o manager · R relaunch · x clear"
	case n.Steer != nil:
		head = pal.yellow.Render(pad("steer", 8))
		what = fmt.Sprintf("%s has not adopted r%d after %d nudges: %s", m.managerName(n.Manager), n.Steer.Revision, n.Steer.Nudges, n.Steer.Text)
		keys = "enter open · o manager · S steer again · R relaunch · x dismiss"
	case n.Relay != nil:
		head = pal.red.Render(pad("refused", 8))
		what = fmt.Sprintf("%s keeps refusing %s, %d times since %s: %s", n.Relay.Dest, relayWhat(*n.Relay), n.Relay.Refusals,
			n.Relay.RefusedSince.Local().Format(time.Kitchen), n.Relay.Error)
		keys = "x drop it"
	case n.PR != nil:
		head, what = pal.yellow.Render(pad("merge", 8)), n.PR.URL
		if st := n.PR.Status; st != nil {
			ci, review := "no checks", "no review required"
			if st.Passed > 0 {
				ci = fmt.Sprintf("ci green, %d passed", st.Passed)
			}
			if st.Review == "APPROVED" {
				review = "approved"
			}
			what = st.Title + pal.dim.Render(fmt.Sprintf("  %s#%d · %s · %s", n.PR.Repo, n.PR.Number, ci, review))
		}
		if n.Manager != "" {
			what = m.managerName(n.Manager) + ": " + what
		}
		keys = "enter open in browser · x dismiss"
	case n.Review != nil:
		head, what = pal.yellow.Render(pad("review", 8)), n.Review.URL
		if n.Review.Title != "" {
			what = n.Review.Title + pal.dim.Render("  by "+n.Review.Author+"  "+n.Review.URL)
		}
		keys = "enter open in browser · x dismiss"
	case n.Doc != nil:
		head, what = pal.yellow.Render(pad("doc", 8)), n.Doc.Title+pal.dim.Render("  "+n.Doc.Asker+": "+n.Doc.Text)
		if len(n.Docs) > 1 {
			head, what = pal.yellow.Render(pad("doc", 8)), fmt.Sprintf("%s  %d open threads", n.Doc.Title, len(n.Docs))+pal.dim.Render("  newest "+n.Doc.Asker+": "+n.Doc.Text)
		}
		keys = "enter open in browser · x dismiss"
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
	m.field(&b, "brief", briefMeta(m.board.At, n)+"  "+core.BriefURL(n.Manager.ID)+pal.dim.Render("  b shows it, W opens the page"))
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
	if f := n.Feed; len(f.PRs)+len(f.Touched) > 0 {
		b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" PULL REQUESTS (%d to land)", len(f.PRs))) + pal.dim.Render("  X unties one") + "\n")
		for i, url := range f.PRs {
			b.WriteString(fmt.Sprintf("   %d. %s\n", i+1, url))
		}
		for i, url := range f.Touched {
			b.WriteString(pal.dim.Render(fmt.Sprintf("   %d. %s  someone else's, context only", len(f.PRs)+i+1, url)) + "\n")
		}
	}
	b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" SESSIONS (%d)", len(m.rows()))) + "\n")
	for i, r := range m.rows() {
		label := ""
		if r.session.Key() == n.Manager.Session {
			label = pal.dim.Render("manager  ")
		}
		b.WriteString(m.mark(i) + label + pad(titleOf(*r.session), 36) + " " + pal.state(stateWord(*r.session)).Render(pad(stateWord(*r.session), 8)) + " " + doingText(m.board.At, r.session.Session) + "\n")
	}
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
	if len(subs)+finished > 0 {
		b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" SUBAGENTS (%d running, %d done)", len(subs), finished)) + "\n")
	}
	for _, w := range subs {
		line := "   " + pad(w.Name, 36) + " " + pal.state(w.State).Render(pad(w.State, 8)) + " " + oneLine(w.Activity) + pal.dim.Render(" "+age(m.board.At, w.At))
		b.WriteString(ansi.Truncate(line, max(20, m.width-1), "…") + "\n")
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
	m.field(&b, "brief", briefMeta(m.board.At, n)+"  "+core.BriefURL(n.Manager.ID)+pal.dim.Render("  b shows it, W opens the page with its reports' briefs"))
	b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" REPORTS (%d)", len(n.Children))) + "\n")
	if len(n.Children) == 0 {
		b.WriteString(pal.dim.Render("   none; c then a on a goal row puts it under a chief") + "\n")
	}
	for i, r := range m.rows() {
		b.WriteString(m.nodeLines(i, *r.node, r.depth))
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
	case "left", "right":
		if (m.mode == modeList && !m.flat || m.mode == modeChief) && m.filter == "" && r.node != nil && expandable(*r.node) {
			if m.collapsed == nil {
				m.collapsed = map[string]bool{}
			}
			if key == "left" {
				m.collapsed[r.node.Manager.ID] = true
			} else {
				delete(m.collapsed, r.node.Manager.ID)
			}
			m.clamp()
		}
		return m, nil, true
	case "n", "N":
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
		id := g.ID
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
			m.message = n.Manager.ID + " has registered no artifact; b shows its brief"
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
	case "s", "r":
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
			if mg.Host != "" {
				return mg.ID + " relaunched on " + m.hostTag(mg.Host), err
			}
			return mg.ID + " relaunched as " + core.Short(mg.Session), err
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
				return g.ID + " recorded; its manager did not start: " + mg.LastError, nil
			}
			return g.ID + " started; " + mg.ID + " owns it", err
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
			return "chief " + c.ID + " over " + plural(len(over), "report"), err
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

// dropPR asks which of the goal's pull requests to untie, and why, then drops it on the host the goal runs on.
func (m *Model) dropPR(n core.Node) {
	prs := append(append([]string(nil), n.Feed.PRs...), n.Feed.Touched...)
	if len(prs) == 0 {
		m.message = n.Goal.ID + " has no pull requests tied to it"
		return
	}
	goal := n.Goal.ID
	why := func(url string) func(Model, string) (tea.Model, tea.Cmd) {
		return func(m Model, text string) (tea.Model, tea.Cmd) {
			m.askLine("why "+url+" is not "+goal+"'s", "", false, func(m Model, why string) (tea.Model, tea.Cmd) {
				if why == "" {
					return m, nil
				}
				c := m.core
				return m, m.do(func() (string, error) {
					if host, rest, ok := core.SplitRemote(goal); ok && n.Goal.Host != "" {
						_, err := c.RunOrRelay(host, "goal", "pr", "drop", rest, url, "--why", why)
						return "dropped " + url + " from " + goal, err
					}
					_, _, err := c.DropGoalPR("", goal, url, why)
					return "dropped " + url + " from " + goal, err
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
	m.askLine(fmt.Sprintf("drop which pull request from %s? its number 1-%d, or its link", goal, len(prs)), "", false, func(m Model, text string) (tea.Model, tea.Cmd) {
		url := text
		if i, err := strconv.Atoi(text); err == nil && i >= 1 && i <= len(prs) {
			url = prs[i-1]
		}
		if !slices.Contains(prs, url) {
			m.message = text + " is not one of " + goal + "'s pull requests"
			return m, nil
		}
		return why(url)(m, "")
	})
}
