package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/drift"
	"github.com/project-kikkuli/yamnaya/internal/progress"
)

func (m Model) View() tea.View {
	page := m.page
	if page == nil {
		prepared := m.prepareView()
		page = &prepared
	}
	key := drawn{page: page, frame: m.peaks.frame, width: m.width, height: m.height, gen: m.board.Gen, shown: m.shown(), dark: m.peaks.dark, minute: time.Now().Truncate(time.Minute), screen: m.screenVer()}
	if m.drawn == nil || m.drawn.key != key || page != m.page {
		text := m.paint(*page)
		if page == m.page && m.drawn != nil {
			*m.drawn = lastDraw{key: key, text: text}
		}
		v := tea.NewView(text)
		v.AltScreen, v.ReportFocus, v.MouseMode = true, true, tea.MouseModeCellMotion
		return v
	}
	v := tea.NewView(m.drawn.text)
	v.AltScreen, v.ReportFocus, v.MouseMode = true, true, tea.MouseModeCellMotion
	return v
}

// drawn is what a drawn screen was drawn from; a View from the same page, frame, size and board draws nothing new.
type drawn struct {
	page          *viewPage
	frame         int
	width, height int
	gen           uint64
	shown, dark   bool
	minute        time.Time
	screen        int
}

func (m Model) screenVer() int {
	if m.peaks.screen == nil {
		return 0
	}
	return m.peaks.screen.ver
}

type lastDraw struct {
	key  drawn
	text string
}

func (m Model) prepareView() viewPage {
	bodyModel := m
	bodyModel.slots = m.hueSlots()
	if len(m.killing) > 0 {
		bodyModel.dyingAt = map[int]bool{}
		for i, r := range m.rows() {
			if m.rowDying(r) {
				bodyModel.dyingAt[i] = true
			}
		}
	}
	bodyModel.width = m.tableWidth()
	var body viewPage
	switch m.mode {
	case modeHelp:
		body = textPage(ansi.Wrap(Help, bodyModel.width, " "), "")
	case modeScreen:
		body = textPage(linkedWrap(m.screen, bodyModel.width), "")
	case modeWhy:
		body = textPage(ansi.Wrap(bodyModel.whyView(), bodyModel.width, " "), "")
	case modePick:
		body = textPage(bodyModel.pickView(), "")
	case modeInput:
		if m.inKind == inNewRepo {
			body = textPage(bodyModel.repoPickView(), "")
			break
		}
		if m.picking() {
			body = textPage(bodyModel.optsView(), "")
			break
		}
		fallthrough
	default:
		switch m.listMode() {
		case modeRepo:
			body = bodyModel.repoView()
		case modeDetail:
			body = bodyModel.detailView()
		case modeSession:
			body = bodyModel.sessionView()
		case modeAgent:
			body = bodyModel.agentView()
		case modeItems:
			body = bodyModel.itemsView()
		case modeGoal:
			body = bodyModel.goalView()
		case modeChief:
			body = bodyModel.chiefView()
		default:
			body = bodyModel.listView()
		}
	}
	lines := textPage(bodyModel.header()+"\n", "").lines
	top := len(lines)
	for len(body.lines) > 0 && body.lines[len(body.lines)-1].text == "" {
		body.lines = body.lines[:len(body.lines)-1]
	}
	lines = append(lines, body.lines...)
	var foot []viewLine
	for _, line := range bodyModel.footer() {
		foot = append(foot, textPage(ansi.Wrap(line, max(20, bodyModel.width-1), " "), "").lines...)
	}
	height := m.height
	if m.shown() {
		height = max(12, m.height-m.height/4)
	}
	room := max(1, height-len(foot))
	if (m.mode == modeScreen || m.mode == modeAgent) && len(lines) > room && room > top {
		start := min(m.scrollY, len(lines)-room)
		lines = append(lines[:top:top], lines[top+start:min(len(lines), start+room)]...)
	} else if len(lines) > room {
		lines = m.scroll(lines, room, top)
	}
	if !m.shown() {
		for len(lines) < room {
			lines = append(lines, viewLine{})
		}
	} else {
		lines = append(lines, viewLine{})
	}
	lines = append(lines, foot...)
	for i := range lines {
		lines[i].text = ansi.Truncate(lines[i].text, max(1, bodyModel.width), "…")
	}
	return viewPage{lines: lines}
}

func (m Model) scroll(lines []viewLine, room, top int) []viewLine {
	if room <= top+1 {
		top = 0
	}
	body, space := lines[top:], room-top
	at := 0
	for i, line := range body {
		if strings.HasPrefix(ansi.Strip(line.text), " >") {
			at = i
		}
	}
	start := max(0, min(at+4-space, len(body)-space))
	return append(lines[:top:top], body[start:start+space]...)
}

func (m Model) header() string {
	if !m.loaded {
		if m.err != nil {
			return " ym  error: " + m.err.Error()
		}
		return " ym  reading sessions…"
	}
	counts := map[core.Status]int{}
	for _, s := range m.board.Sessions {
		counts[s.Status]++
	}
	var states []string
	for _, st := range []core.Status{core.Busy, core.Waiting, core.Idle, core.Unknown} {
		if counts[st] > 0 {
			states = append(states, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	sessions := plural(len(m.board.Sessions), "session")
	if len(states) > 0 {
		sessions += pal.dim.Render(" (" + strings.Join(states, ", ") + ")")
	}
	needs := pal.dim.Render("nothing needs you")
	if n := len(m.board.Needs); n > 0 {
		needs = pal.red.Render(fmt.Sprintf("%d need you", n))
	}
	h := pal.bold.Render(" ym") + "    " + strings.Join([]string{sessions, plural(goalCount(m.board.Tree), "goal"), needs}, "  ")
	if m.err != nil {
		h += "  " + pal.red.Render("refresh failed: "+m.err.Error())
	}
	if note := m.board.PRPace.Note(m.board.At); note != "" {
		h += "\n " + pal.yellow.Render(note)
	}
	if len(m.board.Remotes) > 0 {
		var hosts []string
		for _, r := range m.board.Remotes {
			line := m.hostTag(r.Host) + " " + r.Host + " " + r.StateAt(m.board.At)
			switch {
			case r.Err != nil:
				line = pal.red.Render(line)
			case r.Late(m.board.At):
				line = pal.yellow.Render(line)
			}
			hosts = append(hosts, line)
		}
		h += "\n " + pal.dim.Render("hosts ") + strings.Join(hosts, "   ")
		for _, r := range m.board.Remotes {
			for _, sw := range r.Swollen {
				h += "\n " + pal.red.Render(m.hostTag(r.Host)+" "+sw.Line())
			}
		}
	}
	if m.core != nil && m.core.Meter != nil {
		h += "\n" + fleetLine(m.board.Fleet, m.board.At)
	}
	return h + machineAlerts(m.board.Machine, m.width)
}

// goalCount is how many goals the tree shows.
func goalCount(ns []core.Node) int {
	total := 0
	for _, n := range ns {
		total += n.Count()
	}
	return total
}

// machineAlerts are the header's warnings: throttling, an overloaded machine's top consumers and processes holding too much memory.
func machineAlerts(mc core.Machine, width int) string {
	fit := func(s string) string { return ansi.Truncate(s, max(20, width-1), "…") }
	h := ""
	if line := mc.Slots.Pressure.Line(); line != "" {
		h += "\n" + pal.yellow.Render(fit(" "+line))
	}
	if o := mc.Overload(); o != "" {
		h += "\n" + pal.red.Render(fit(" "+o))
	}
	for _, s := range mc.Swollen {
		h += "\n" + pal.red.Render(fit(" "+s.Line()))
	}
	return h
}

// machineLines are the slot summary, each holder, the waiters on one line, and when overloaded the top consumers.
func machineLines(mc core.Machine, now time.Time, width int) string {
	if mc.Slots.Capacity == 0 {
		return ""
	}
	fit := func(s string) string { return ansi.Truncate(s, max(20, width-1), "…") }
	h := "\n" + pal.dim.Render(fit(" "+mc.Summary()))
	if line := mc.Slots.Pressure.Line(); line != "" {
		h += "\n" + pal.yellow.Render(fit(" "+line))
	}
	for _, u := range mc.Holders {
		h += "\n" + fit(fmt.Sprintf("   slot %d  %s", u.Slot, u.Line(now)))
	}
	if len(mc.Waiters) > 0 {
		var ws []string
		for _, u := range mc.Waiters {
			ws = append(ws, u.Line(now))
		}
		h += "\n" + pal.yellow.Render(fit("   waiting  "+strings.Join(ws, " · ")))
	}
	if o := mc.Overload(); o != "" {
		h += "\n" + pal.red.Render(fit(" "+o))
	}
	for _, s := range mc.Swollen {
		h += "\n" + pal.red.Render(fit(" "+s.Line()))
	}
	return h
}

func (m Model) footer() []string {
	var lines []string
	if m.mode == modeInput && m.inKind == inAdopt {
		lines = append(lines, " "+pal.bold.Render("move "+m.inLabel+" into ym?")+" it ends the process in its current terminal")
		lines = append(lines, pal.dim.Render(" enter yes   esc no"))
		return lines
	}
	if m.mode == modeConfirm {
		lines = append(lines, " "+pal.bold.Render(m.ask)+" y/n")
		return lines
	}
	if m.mode == modeInput && m.picking() {
		keys := " ↑↓ choose · enter put it there · esc cancel"
		switch m.inKind {
		case inOver:
			keys = " ↑↓ choose · space check · enter go on to its name · esc cancel"
		case inChiefMenu:
			keys = " ↑↓ choose · enter or its letter picks · esc cancel"
		}
		return append(lines, pal.dim.Render(keys))
	}
	if m.mode == modeInput {
		repo := filepath.Base(m.newL.Repo)
		label := map[inputKind]string{inAnswer: "answer " + m.inLabel, inDismiss: "dismiss " + m.inLabel + " because", inRename: "rename to", inFilter: "filter",
			inDismissStale: "dismiss every stale item because", inProfile: "login profile id for " + m.inLabel + " (ym profiles lists logins)", inSend: "send " + m.inLabel,
			inNew: "new " + m.newL.Harness + " session in " + repo + ", prompt", inNewRepo: m.pickLabel(),
			inPhases: "phases of " + m.inLabel + " (a, b, c)", inVibe: "vibe of " + m.inLabel + " (any fitting place: why)",
			inGoal: "new " + m.newL.Harness + " goal in " + repo + ", in your words", inChief: "new " + m.newL.Harness + " " + m.inLabel + ", its name",
			inRemit: "new " + m.newL.Harness + " chief " + m.chiefName + ", its remit in your words (empty: none)",
			inSteer: "send to " + m.inLabel}[m.inKind]
		if m.inKind == inSteer && m.steerHasChief() {
			if m.subtree {
				label += pal.dim.Render(" and everyone below [tab: just them]")
			} else {
				label += pal.dim.Render(" [tab: whole subtree]")
			}
		}
		if m.launching() {
			label += pal.dim.Render(" [tab: " + core.Other(m.newL.Harness) + "] [shift-tab profile: " + func() string {
				if m.newL.Profile == "" {
					return "auto"
				}
				return m.newL.Profile
			}() + "]")
		}
		if m.launchOn != "" && m.launching() {
			label += " on " + m.hostTag(m.launchOn)
		}
		on := m.targetLine()
		if m.fresh {
			lines = append(lines, " "+pal.bold.Render(label+":")+" "+pal.dim.Render(string(m.input))+"█")
			if on != "" {
				lines = append(lines, on)
			}
			lines = append(lines, pal.dim.Render(" enter accepts the default   type to replace it   esc cancel"))
			return lines
		}
		lines = append(lines, " "+pal.bold.Render(label+":")+" "+string(m.input)+"█")
		if on != "" {
			lines = append(lines, on)
		}
		if m.inKind == inNewRepo {
			lines = append(lines, pal.dim.Render(" type to filter   ↑↓ choose   enter pick   esc cancel   ctrl+u clear"))
			return lines
		}
		lines = append(lines, pal.dim.Render(" enter save   esc cancel   ctrl+u clear"))
		return lines
	}
	if m.message != "" {
		lines = append(lines, " "+m.message)
	}
	if m.filter != "" {
		lines = append(lines, pal.dim.Render(" filter: "+m.filter+"   esc clears"))
	}
	for _, l := range wrapHints(" ", m.hints(), m.width) {
		lines = append(lines, pal.dim.Render(l))
	}
	return lines
}

// hintSep separates the keys of a hint line.
const hintSep = " · "

// wrapHints breaks hints, keys hintSep apart, into lines of at most width, each starting with indent, never inside a key.
func wrapHints(indent, hints string, width int) []string {
	var lines []string
	line := ""
	for _, hint := range strings.Split(strings.Trim(hints, " ·"), hintSep) {
		if line != "" && ansi.StringWidth(indent+line+hintSep+hint) > max(20, width-1) {
			lines = append(lines, indent+line)
			line = ""
		}
		if line != "" {
			line += hintSep
		}
		line += hint
	}
	return append(lines, indent+line)
}

// joinHints joins hint lists, skipping empty ones.
func joinHints(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, hintSep)
}

// boardHints are the board's footer keys; ? lists the rest.
const boardHints = "↑↓ move · tab section · [ ] repo · < > top/bottom · enter open · r reply · g go · K kill · c chief · b brief · n new · / filter · ? more"

func (m Model) hints() string {
	if r, ok := m.selected(); ok && r.item != nil && r.item.Action == "chat" && (m.mode == modeList || m.mode == modeDetail || m.mode == modeSession || m.mode == modeItems) {
		return "o open chat · O beside · enter context · l later · x dismiss · ? more"
	}
	if m.mode == modeList {
		return boardHints
	}
	controls := m.sessionHints()
	if m.width < 80 && (m.mode == modeRepo || m.mode == modeDetail || m.mode == modeSession) {
		return joinHints(controls, "↑↓ move · enter open · ? more")
	}
	switch m.mode {
	case modeHelp, modeScreen:
		return "esc back"
	case modeItems:
		return "↑↓ move · enter expand · r reply · g go ahead · l later · u unsnooze · x dismiss · esc back"
	case modeWhy:
		return "r reply · g go ahead · l later · x dismiss · u unsnooze · o go to its session · O beside · esc back"
	case modePick:
		return "↑↓ choose · enter open · esc back"
	case modeMerge:
		return "↑↓ choose mission · enter merge into it · esc cancel"
	case modeRepo:
		return joinHints(controls, "↑↓ move · < > top/bottom · enter open mission · o go to session · r reply · n new · e rename · m merge · esc back · ? more")
	case modeDetail:
		return joinHints(controls, "↑↓ move · < > top/bottom · enter open · o go to session · r reply · g go · x dismiss · esc back · ? more")
	case modeSession:
		return joinHints(controls, "↑↓ move · < > top/bottom · enter open agent or item · o go to session · r reply · esc back · ? more")
	case modeAgent:
		return joinHints(controls, "↑↓ scroll · space/b page · < > top/bottom · o go to its session · O beside · esc back")
	case modeGoal:
		return joinHints(controls, "↑↓ move · enter open session · o go to session · r reply · b brief · c chief · S steer plan · R relaunch · esc back · ? more")
	case modeChief:
		return "↑↓ move · < > top/bottom · enter open · r reply · b brief · c chief · S steer plan · R relaunch · esc back · ? more"
	}
	return boardHints
}

func (m Model) mark(i int) string {
	switch {
	case m.dyingAt[i] && i == m.cursor:
		return pal.red.Render(" >x")
	case m.dyingAt[i]:
		return pal.red.Render("  x")
	case i == m.cursor:
		return " > "
	}
	return "   "
}

func (m Model) listView() viewPage {
	if !m.loaded {
		return viewPage{}
	}
	b := viewPage{}
	rows := m.rows()
	needs := 0
	for _, r := range rows {
		if r.need != nil && !m.isStale(r.need) {
			needs++
		}
	}
	b.WriteString(pal.bold.Render(fmt.Sprintf(" NEEDS YOU (%d)", needs)) + "\n")
	if needs == 0 {
		b.WriteString(pal.dim.Render("   nothing") + "\n")
	}
	goals, repos := false, false
	group := ""
	cols := m.missionCols(2)
	for i, r := range rows {
		if r.node != nil && !goals {
			goals = true
			title := pal.bold.Render(" GOALS") + pal.dim.Render("  by repository, chiefs with their reports; t lists every goal by its own")
			if m.flat {
				title = pal.bold.Render(" GOALS") + pal.dim.Render("  every goal by its own repository; t nests reports under their chiefs")
			}
			b.WriteString("\n" + title + "\n")
		}
		if r.node != nil && r.group != group {
			group = r.group
			b.WriteString(pal.bold.Render("   "+groupLabel(group)) + "\n")
		}
		if (r.repo != nil || r.project != nil) && !repos {
			repos = true
			if !goals {
				goals = true
				b.WriteString("\n" + pal.bold.Render(" GOALS") + "\n" + pal.dim.Render("   none; n hands a goal to a new manager") + "\n")
			}
			b.WriteString("\n" + pal.bold.Render(" SESSIONS YOU STARTED") + "\n")
			if cols.bar > 0 {
				b.WriteString(pal.dim.Render("   "+progressLegend) + "\n")
			}
		}
		switch {
		case r.stale:
			line := m.mark(i) + pal.dim.Render(fmt.Sprintf("stale (%d)", len(m.board.Stale))) + pal.dim.Render(" open items whose sessions ended")
			if i == m.cursor {
				line += pal.dim.Render("   enter expand · x dismiss all")
			}
			b.WriteString(line + "\n")
		case r.node != nil:
			b.WriteString(withNote(m.nodeLines(i, *r.node, r.depth), m.rowNote(r)))
		case r.need != nil:
			if text, ok := m.queueLines(i, *r.need); ok {
				b.WriteString(text)
			} else {
				b.WriteString(withNote(m.needLines(i, *r.need), m.rowNote(r)))
			}
		case r.item != nil:
			b.WriteString(m.needLines(i, core.Need{Item: r.item, Project: m.board.ProjectOf(r.item.Session)}))
		case r.project == nil:
			b.WriteString(m.repoLine(i, *r.repo, cols))
		default:
			b.WriteString(withNote(m.missionLine(i, *r.project, cols), m.rowNote(r)))
		}
	}
	if !goals {
		b.WriteString("\n" + pal.bold.Render(" GOALS") + "\n" + pal.dim.Render("   none; n hands a goal to a new manager") + "\n")
	}
	if !repos {
		b.WriteString("\n" + pal.bold.Render(" SESSIONS YOU STARTED") + "\n" + pal.dim.Render("   none") + "\n")
	}
	return b
}

func (m Model) isStale(n *core.Need) bool {
	for i := range m.board.Stale {
		if n == &m.board.Stale[i] {
			return true
		}
	}
	return false
}

func (m Model) needsIn(r core.Repo) int {
	keys := map[string]bool{}
	for _, p := range r.Missions {
		keys[p.Key] = true
	}
	n := 0
	for _, need := range m.board.Needs {
		if keys[need.Project] {
			n++
		}
	}
	return n
}

// vibeLines are a mission's latest vibe, who set it, and its recent place changes.
func (m Model) vibeLines(p core.Project) string {
	v, ok := p.Vibe()
	if !ok {
		return ""
	}
	place := pal.bold.Render(v.Place)
	if p.VibeStale(m.board.At) {
		place = pal.dim.Render(v.Place)
	}
	line := " vibe    " + pal.dim.Render("says: ") + place
	if v.Why != "" {
		line += "  " + v.Why
	}
	by := age(m.board.At, v.At)
	if v.Session != "" {
		by = v.Harness + " " + core.Short(v.Session) + ", " + by
	}
	line += pal.dim.Render("  " + by)
	if ps := p.Places(5); len(ps) > 1 {
		var hist []string
		for _, h := range ps {
			hist = append(hist, h.Place+pal.dim.Render(" "+age(m.board.At, h.At)))
		}
		line += "\n" + pal.dim.Render(" was     ") + strings.Join(hist[1:], pal.dim.Render(" ← "))
	}
	return line + "\n"
}

// nameWidth gives mission names what the fixed columns leave, between 20 and 44 cells.
func (m Model) nameWidth() int {
	return min(44, max(20, m.width/3))
}

func sessionCounts(sessions []core.Live) string {
	counts := map[core.Status]int{}
	for _, s := range sessions {
		counts[s.Status]++
	}
	var parts []string
	for _, st := range []core.Status{core.Waiting, core.Busy, core.Idle} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	out := plural(len(sessions), "session")
	if len(parts) > 0 {
		out += " (" + strings.Join(parts, ", ") + ")"
	}
	return out
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// prsOf lists the pull requests of the sessions and their crews once each.
func prsOf(sessions []core.Live) []core.PR {
	seen := map[string]bool{}
	var out []core.PR
	for _, s := range sessions {
		for _, pr := range s.AllPRs() {
			if !seen[pr.URL] {
				seen[pr.URL] = true
				out = append(out, pr)
			}
		}
	}
	return out
}

// prSummary counts open pull requests by CI result, then merged ones.
func prSummary(t core.PRCount) string {
	open, fail, run, pass, merged := t.Open, t.Failing, t.Running, t.Green, t.Merged
	var parts []string
	if open > 0 {
		s := plural(open, "PR")
		var ci []string
		if fail > 0 {
			ci = append(ci, pal.red.Render(fmt.Sprintf("✗%d", fail)))
		}
		if run > 0 {
			ci = append(ci, pal.yellow.Render(fmt.Sprintf("…%d", run)))
		}
		if pass > 0 {
			ci = append(ci, pal.green.Render(fmt.Sprintf("✓%d", pass)))
		}
		if len(ci) > 0 {
			s += " " + strings.Join(ci, " ")
		}
		parts = append(parts, s)
	}
	if merged > 0 {
		parts = append(parts, fmt.Sprintf("%d merged", merged))
	}
	return strings.Join(parts, ", ")
}

// doingText is the last tool action and how long ago it ran.
func doingText(now time.Time, s core.Session) string {
	if s.Doing.Tool == "" {
		return ""
	}
	return s.Doing.String() + pal.dim.Render(" "+age(now, s.Doing.At))
}

func memberDoing(now time.Time, c core.Member) string {
	if c.Doing.Tool == "" {
		return ""
	}
	return c.Doing.String() + " " + age(now, c.Doing.At)
}

// spend is what the sessions cost at API prices, subagents and child threads included, or "" until usage is read.
// A session started by another in the list counts only through it.
func (m Model) spend(sessions ...core.Live) string {
	f := m.board.Fleet
	if !f.Ready || len(sessions) == 0 {
		return ""
	}
	in := map[string]bool{}
	for _, s := range sessions {
		if _, known := f.Sessions[s.UsageKey()]; s.Remote != "" && !known {
			return "usage unknown"
		}
		in[s.UsageKey()] = true
	}
	var total core.Spend
	for key := range in {
		counted := false
		seen := map[string]bool{key: true}
		for at := f.Sessions[key].Parent; at != "" && !seen[at]; at = f.Sessions[at].Parent {
			seen[at] = true
			if in[at] {
				counted = true
				break
			}
		}
		if !counted {
			total = total.Plus(f.Sessions[key].Total)
		}
	}
	return spendText(f, total)
}

// missionSpend is a mission's spend; a repo's is its missions' sum.
func (m Model) missionSpend(ps ...core.Project) string {
	if !m.board.Fleet.Ready {
		return ""
	}
	var total core.Spend
	for _, p := range ps {
		if p.Stats.Spend == nil && core.ProjectHost(p) != "" {
			return "usage unknown"
		}
		total = total.Plus(p.Spend)
	}
	return spendText(m.board.Fleet, total)
}

// memberSpend is one crew member's own spend: a Claude subagent's transcript, or a Codex child thread with its own children.
func (m Model) memberSpend(s core.Live, c core.Member) string {
	f := m.board.Fleet
	if !f.Ready {
		return ""
	}
	if sp, ok := f.Agents[s.UsageKey()+"/"+c.ID]; ok {
		return spendText(f, sp)
	}
	child := s
	child.ID = c.ID
	if ss, ok := f.Sessions[child.UsageKey()]; ok {
		return spendText(f, ss.Total)
	}
	if s.Remote != "" {
		return "usage unknown"
	}
	return core.Spend{}.Dollars()
}

func (m Model) needLines(i int, n core.Need) string {
	where := m.projectName(n.Project)
	if n.Item != nil && n.Item.Dossier.Goal != "" {
		where = n.Item.Dossier.Goal + " " + oneLine(n.Item.Dossier.GoalAsk)
	}
	name := pad(m.tagged(m.needHost(n), where), min(32, max(16, m.width/4)))
	sel := i == m.cursor
	var b strings.Builder
	if n.Item == nil {
		what := "permission prompt"
		if n.Session.Prompt != nil {
			what = n.Session.Prompt.String()
		}
		b.WriteString(m.mark(i) + pal.yellow.Render("PERM ") + " " + name + " " + what + "\n")
		if sel {
			b.WriteString(pal.dim.Render("         enter "+goVerb(*n.Session)+" · r reply") + "\n")
		}
		return b.String()
	}
	it := n.Item
	q, keys := it.Question, "enter expand · r reply · l later · x dismiss · o go to session · O beside"
	if it.Action == "chat" {
		q, keys = pal.yellow.Render("action in chat · ")+q, "enter context · o open chat · O beside · l later · x dismiss"
	}
	if it.Kind == core.KindGo {
		q, keys = pal.yellow.Render("go ahead? ")+q, "enter expand · g go ahead · r reply · l later · x dismiss · o go to session · O beside"
	}
	if len(core.Unprepared(*it)) > 0 {
		q = pal.yellow.Render("unprepared · ") + q
	}
	if more := len(n.Asks) - 1; more > 0 {
		q += pal.dim.Render(fmt.Sprintf("  +%d more", more))
		keys = "enter list all   " + strings.TrimPrefix(keys, "enter expand   ")
	}
	b.WriteString(m.mark(i) + m.hue(n.Project).Render(pad(m.itemID(*it), m.idWidth())) + " " + name + " " + q + "\n")
	var meta []string
	if it.Rec != "" {
		meta = append(meta, "rec: "+it.Rec)
	}
	meta = append(meta, fmt.Sprintf("blocks %d", it.Blocks), "src: "+m.sourceName(*it), age(m.board.At, it.CreatedAt))
	if sel {
		b.WriteString(pal.dim.Render("         "+strings.Join(meta, " | ")) + "\n")
		if line := m.unprepared(*it); line != "" {
			b.WriteString(pal.yellow.Render("         "+ansi.Truncate(line, max(10, m.width-10), "…")) + "\n")
		}
		if line := evidenceLine(*it); line != "" {
			b.WriteString(pal.dim.Render("         "+ansi.Truncate(line, max(10, m.width-10), "…")) + "\n")
		}
		b.WriteString(pal.dim.Render("         "+keys) + "\n")
	}
	return b.String()
}

// itemID names an item, with its project where the needs list mixes projects.
func (m Model) itemID(it core.Item) string {
	if m.mixed() {
		return it.Qualified()
	}
	return it.ID
}

func (m Model) mixed() bool {
	scopes := map[string]bool{}
	for _, ns := range [][]core.Need{m.board.Needs, m.board.Stale} {
		for _, n := range ns {
			if n.Item != nil {
				scopes[n.Item.Scope] = true
			}
		}
	}
	return len(scopes) > 1
}

func (m Model) idWidth() int {
	w := 5
	if m.mixed() {
		for _, ns := range [][]core.Need{m.board.Needs, m.board.Stale} {
			for _, n := range ns {
				if n.Item != nil {
					w = max(w, len(n.Item.Qualified()))
				}
			}
		}
	}
	return min(w, 24)
}

func (m Model) sourceName(it core.Item) string {
	if mg, ok := m.board.Org.ManagerOf(it.Session); ok {
		return mg.Label()
	}
	if s, ok := m.board.Session(it.Session); ok && s.Title != "" {
		return ansi.Truncate(oneLine(s.Title), 60, "…")
	}
	return it.Harness + " " + core.Short(it.Session)
}

func copiesText(p core.Project) string {
	for _, s := range p.Sessions {
		if len(s.Copies) > 1 {
			return fmt.Sprintf("%d COPIES", len(s.Copies))
		}
	}
	return ""
}

func copiesWhere(p core.Project) string {
	for _, s := range p.Sessions {
		if len(s.Copies) > 1 {
			var at []string
			for _, c := range s.Copies {
				at = append(at, fmt.Sprintf("pid %d %s", c.PID, strings.TrimPrefix(c.TTY, "/dev/")))
			}
			return strings.Join(at, ", ")
		}
	}
	return ""
}

func (m Model) repoView() viewPage {
	r, ok := m.board.Repo(m.repo)
	if !ok {
		return textPage(pal.dim.Render("   this repo has no live sessions now; esc back"), "")
	}
	b := viewPage{place: "r:" + r.Key}
	key := r.Key
	if key == "" {
		key = "sessions outside any git repo"
	}
	b.WriteString(pal.bold.Render(" "+r.Name) + pal.dim.Render("  "+key) + "\n")
	b.WriteString(" " + m.repoCounts(r) + "\n")
	b.WriteString("\n" + pal.bold.Render(" MISSIONS") + "\n")
	cols := m.missionCols(0)
	for i, p := range r.Missions {
		b.place = "m:" + p.Key
		b.WriteString(m.missionLine(i, p, cols))
		if len(p.Sessions) > 0 {
			lead := p.Sessions[0]
			var meta []string
			if lead.Ask != "" {
				meta = append(meta, "ask: "+lead.Ask)
			}
			meta = append(meta, lead.Location())
			if lead.Branch != "" {
				meta = append(meta, lead.Branch)
			}
			b.WriteString(pal.dim.Render("     "+strings.Join(meta, " | ")) + "\n")
		}
	}
	return b
}

func (m Model) detailView() viewPage {
	p, ok := m.board.Project(m.detail)
	if !ok {
		return textPage(pal.dim.Render("   this mission has no live sessions now; esc back"), "")
	}
	b := viewPage{place: "m:" + p.Key}
	b.WriteString(pal.bold.Render(" "+p.Name) + pal.dim.Render("  "+p.RepoName+"  "+p.Key) + "\n")
	if len(p.Phases.Names) > 0 {
		var ps []string
		for j, name := range p.Phases.Names {
			switch {
			case j < p.Phases.Current:
				ps = append(ps, pal.dim.Render("✓ "+name))
			case j == p.Phases.Current:
				ps = append(ps, pal.bold.Render("▶ "+name))
			default:
				ps = append(ps, "○ "+name)
			}
		}
		b.WriteString(" phases  " + strings.Join(ps, "  ") + "\n")
	}
	if pr := prSummary(p.Stats.PRs); pr != "" {
		b.WriteString(" prs     " + pr + "\n")
	}
	if core.ProjectHost(p) != "" {
		if f := p.Stats.Done; f != nil {
			b.WriteString(" progress " + progress.Bar(*f, 20) + fmt.Sprintf(" %3.0f%%", *f*100) + pal.dim.Render("  as "+core.ProjectHost(p)+" reads it") + "\n")
		}
	} else {
		pg := progress.Of(p)
		if f, ok := pg.Fraction(); ok {
			b.WriteString(" progress " + progress.Bar(f, 20) + fmt.Sprintf(" %3.0f%%  ", f*100) + pal.dim.Render(pg.Parts()) + "\n")
		}
		b.WriteString(units(pg.Units))
	}
	b.WriteString(m.vibeLines(p))
	b.WriteString("\n" + pal.bold.Render(" SESSIONS AND CREW") + "\n")
	i, wide := 0, m.whereWidth(p.Sessions)
	for _, s := range p.Sessions {
		title := s.Title
		if title == "" {
			title = core.Short(s.Key())
		}
		line := m.mark(i) + pad(title, m.nameWidth()) + " " + pad(profileDisplay(s.Harness, s.Profile), 7) + pal.state(stateWord(s)).Render(pad(stateWord(s), 9)) + pad(m.where(s), wide)
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
			line += pal.dim.Render("   o adopt")
		}
		b.place = "s:" + s.Key()
		b.WriteString(withNote(line+"\n", m.note(sessionKey(s))))
		if d := doingText(m.board.At, s.Session); d != "" {
			b.WriteString(pal.dim.Render("     now: ") + d + "\n")
		}
		i++
		for _, c := range s.Crew {
			b.place = "a:" + s.Key() + "/" + c.ID
			b.WriteString(m.crewLine(i, s, c))
			i++
		}
	}
	b.place = "m:" + p.Key
	for _, d := range p.Dormant {
		title := d.Title
		if title == "" {
			title = d.Key
		}
		host, key := m.splitHost(d.Key)
		line := m.mark(i) + pad(title, m.nameWidth()) + " " + pad(d.Harness, 7) + pal.dim.Render(pad(d.State, 9)) + pad(m.tagged(host, key), wide) + " " + d.Branch
		if i == m.cursor {
			line += pal.dim.Render("   enter revive")
			if ansi.StringWidth(d.State) > 9 {
				line += "   " + pal.red.Render(d.State)
			}
		}
		b.WriteString(withNote(line+"\n", m.note(d.Key)))
		i++
	}
	b.WriteString("\n" + pal.bold.Render(" ITEMS") + "\n")
	if len(p.Items) == 0 {
		b.WriteString(pal.dim.Render("   none") + "\n")
	}
	ids := itemIDs(p.Items)
	for _, it := range p.Items {
		b.WriteString(m.itemLines(i, it, ids))
		i++
	}
	return b
}

func (m Model) crewLine(i int, s core.Live, c core.Member) string {
	kind := c.Type
	if kind == "" {
		kind = "subagent"
		if c.Teammate {
			kind = "teammate"
		}
	}
	l := m.mark(i) + "  └ " + pad(c.Name, max(16, m.nameWidth()-4)) + " " + pad(kind, 16) + " " + pal.state(memberState(c)).Render(pad(memberState(c), 8)) + " " + pad(shortModel(c.Model), 10)
	if sp := m.memberSpend(s, c); sp != "" {
		l += " " + pad(sp, 8)
	}
	if c.Status != core.Done {
		l += " " + memberDoing(m.board.At, c)
	}
	if c.Status != core.Busy && i != m.cursor {
		l = pal.dim.Render(l)
	}
	return l + "\n"
}

// itemIDs labels each item by its bare ID, or by project and ID when the items come from more than one project, padded to one width.
func itemIDs(items []core.Item) map[string]string {
	label := func(it core.Item) string { return it.ID }
	for _, it := range items {
		if it.Scope != items[0].Scope {
			label = core.Item.Qualified
			break
		}
	}
	w := 5
	for _, it := range items {
		w = max(w, len(label(it)))
	}
	ids := map[string]string{}
	for _, it := range items {
		ids[it.Key()] = pad(label(it), w)
	}
	return ids
}

func (m Model) itemLines(i int, it core.Item, ids map[string]string) string {
	line := m.mark(i) + ids[it.Key()] + " " + pad(it.State, 10) + " " + m.tagged(it.Host, it.Question)
	if it.State != core.ItemOpen && i != m.cursor {
		line = pal.dim.Render(line)
	}
	out := line + "\n"
	if ev := evidenceLine(it); ev != "" && i == m.cursor {
		out += pal.dim.Render("         "+ansi.Truncate(ev, max(10, m.width-10), "…")) + "\n"
	}
	if it.Answer != "" {
		out += pal.dim.Render("         answer: "+it.Answer+delivered(it)) + "\n"
	}
	if it.Reason != "" {
		out += pal.dim.Render("         dismissed: "+it.Reason+delivered(it)) + "\n"
	}
	return out
}

func shortModel(model string) string {
	model = strings.TrimPrefix(model, "claude-")
	if i := strings.LastIndex(model, "-20"); i > 0 {
		model = model[:i]
	}
	return model
}

// field writes a labelled value, wrapping long values under their label.
// adriftText lists every agent on the board going nowhere now, with its evidence, ahead of the health report.
func adriftText(b core.Board) string {
	var lines []string
	add := func(who string, signs []drift.Sign) {
		for _, s := range signs {
			lines = append(lines, "  "+who+"  "+s.String())
		}
	}
	for _, s := range b.Sessions {
		who := s.Title
		if who == "" {
			who = core.Short(s.Key())
		}
		add(who, s.Drift)
		for _, c := range s.Crew {
			add(who+" / "+c.Name, c.Drift)
		}
	}
	if len(lines) == 0 {
		return "adrift now: none\n\n"
	}
	return "adrift now (ym health drift lists past episodes)\n" + strings.Join(lines, "\n") + "\n\n"
}

// stateWord is how a session's row reads: adrift when it shows a sign of going nowhere, else its status.
func stateWord(s core.Live) string {
	if s.Off {
		return core.StandOff
	}
	if len(s.Drift) > 0 {
		return "adrift"
	}
	return s.Status.String()
}

func memberState(c core.Member) string {
	if len(c.Drift) > 0 {
		return "adrift"
	}
	return c.Status.String()
}

func signLines(signs []drift.Sign) string {
	var out []string
	for _, s := range signs {
		out = append(out, s.String())
	}
	return strings.Join(out, "\n")
}

func (m Model) field(b interface{ WriteString(string) (int, error) }, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	width := max(20, m.width-14)
	lines := strings.Split(ansi.Wrap(value, width, " "), "\n")
	for i, l := range lines {
		if i == 0 {
			b.WriteString(" " + pal.bold.Render(pad(label, 11)) + " " + l + "\n")
		} else {
			b.WriteString("             " + l + "\n")
		}
	}
}

// clip keeps the first n lines of text.
func clip(text string, n int) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	if len(lines) > n {
		return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-n)
	}
	return strings.Join(lines, "\n")
}

func (m Model) sessionView() viewPage {
	s, ok := m.session()
	if !ok {
		return textPage(pal.dim.Render("   this session is gone; esc back"), "")
	}
	b := viewPage{place: "s:" + s.Key()}
	title := s.Title
	if title == "" {
		title = core.Short(s.Key())
	}
	b.WriteString(pal.bold.Render(" "+title) + pal.dim.Render("  "+profileDisplay(s.Harness, s.Profile)+" "+s.Key()) + "\n\n")
	status := s.Status.String()
	if !s.Since.IsZero() {
		status += " since " + age(m.board.At, s.Since)
	}
	if s.WaitingFor != "" {
		status += " (" + s.WaitingFor + ")"
	}
	m.field(&b, "status", status)
	m.field(&b, "adrift", signLines(s.Drift))
	m.field(&b, "where", s.Location())
	m.field(&b, "repo", strings.TrimSpace(s.Repo+" "+s.Branch))
	if s.Worktree != "" && s.Worktree != s.Cwd {
		m.field(&b, "worktree", s.Worktree)
	}
	m.field(&b, "cwd", s.Cwd)
	m.field(&b, "model", s.Model)
	m.field(&b, "spend", m.spend(*s))
	m.field(&b, "task", clip(s.Task, 4))
	if s.Ask != s.Task {
		m.field(&b, "latest ask", clip(s.Ask, 4))
	}
	if s.Doing.Tool != "" {
		m.field(&b, "doing now", s.Doing.String()+"  "+age(m.board.At, s.Doing.At))
	}
	m.field(&b, "last reply", clip(s.Result, 6))
	if prs := s.AllPRs(); len(prs) > 0 {
		b.WriteString("\n" + pal.bold.Render(" PULL REQUESTS") + "\n")
		for _, pr := range prsOf([]core.Live{*s}) {
			b.WriteString("   " + pad(fmt.Sprintf("%s#%d", pr.Repo, pr.Number), 34) + " " + prStatus(pr) + "\n")
		}
	}
	if us := progress.Units([]core.Live{*s}); len(us) > 0 {
		b.WriteString("\n" + pal.bold.Render(" PIPELINE") + "\n" + units(us))
	}
	b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" CREW (%d)", len(s.Crew))) + "\n")
	i := 0
	for _, c := range s.Crew {
		b.place = "a:" + s.Key() + "/" + c.ID
		b.WriteString(m.crewLine(i, *s, c))
		i++
	}
	b.place = "s:" + s.Key()
	if len(s.Crew) == 0 {
		b.WriteString(pal.dim.Render("   none") + "\n")
	}
	if len(s.Messages) > 0 {
		b.WriteString("\n" + pal.bold.Render(" MESSAGES") + "\n")
		for _, msg := range s.Messages {
			state := map[string]string{core.MsgPending: "pending", core.MsgTyped: "typed", core.MsgUncertain: "submission uncertain (not replayed)", core.MsgInbox: "via inbox", core.MsgSent: "sent"}[msg.State]
			when := age(m.board.At, msg.CreatedAt)
			if !msg.Delivered.IsZero() {
				when = "delivered " + age(m.board.At, msg.Delivered)
				if msg.State == core.MsgUncertain {
					when = "attempted " + age(m.board.At, msg.Delivered)
				}
			}
			line := "   " + pad(state, 10) + " " + msg.Text + pal.dim.Render("  "+when)
			if msg.State != core.MsgPending {
				line = pal.dim.Render(line)
			}
			b.WriteString(line + "\n")
		}
	}
	if p, ok := m.board.Project(m.detail); ok {
		var items []core.Item
		for _, it := range p.Items {
			if it.Session == s.Key() {
				items = append(items, it)
			}
		}
		if len(items) > 0 {
			b.WriteString("\n" + pal.bold.Render(" ITEMS") + "\n")
			ids := itemIDs(items)
			for _, it := range items {
				b.WriteString(m.itemLines(i, it, ids))
				i++
			}
		}
	}
	return b
}

// units is one line per branch of work: its gate strip and the raw facts behind it.
func units(us []progress.Unit) string {
	var b strings.Builder
	for _, u := range us {
		b.WriteString("   " + pad(u.Label, 34) + " " + progress.Strip(u) + pal.dim.Render("  "+u.Detail) + "\n")
	}
	return b.String()
}

func prStatus(pr core.PR) string {
	if pr.Status == nil {
		return pal.dim.Render("status not fetched yet")
	}
	st := pr.Status.String()
	switch {
	case pr.Status.Closed():
	case pr.Status.CI() == "failing":
		st = pal.red.Render(st)
	case pr.Status.CI() == "passing":
		st = pal.green.Render(st)
	}
	if pr.Status.Title != "" {
		st += pal.dim.Render("  " + pr.Status.Title)
	}
	return st
}

func (m Model) agentView() viewPage {
	s, c, ok := m.member()
	if !ok {
		return textPage(pal.dim.Render("   this agent is gone; esc back"), "")
	}
	b := viewPage{place: "a:" + s.Key() + "/" + c.ID}
	b.WriteString(pal.bold.Render(" "+c.Name) + pal.dim.Render("  in "+s.Title+"  "+c.ID) + "\n\n")
	kind := c.Type
	if c.Teammate {
		kind = strings.TrimSpace(kind + " teammate")
	}
	m.field(&b, "type", kind)
	if c.Description != c.Name {
		m.field(&b, "description", c.Description)
	}
	status := c.Status.String()
	if !c.Last.IsZero() {
		status += ", last active " + age(m.board.At, c.Last)
	}
	m.field(&b, "status", status)
	m.field(&b, "adrift", signLines(c.Drift))
	m.field(&b, "model", c.Model)
	m.field(&b, "spend", m.memberSpend(*s, *c))
	m.field(&b, "worktree", c.Worktree)
	if c.Doing.Tool != "" {
		label := "doing now"
		if c.Status != core.Busy {
			label = "last action"
		}
		m.field(&b, label, c.Doing.String()+"  "+age(m.board.At, c.Doing.At))
	}
	m.field(&b, "tools", toolCounts(c.Tools))
	if len(c.PRs) > 0 {
		var prs []string
		for _, pr := range c.PRs {
			prs = append(prs, fmt.Sprintf("%s#%d %s", pr.Repo, pr.Number, prStatus(pr)))
		}
		m.field(&b, "prs", strings.Join(prs, "\n"))
	}
	if len(c.Files) > 0 {
		var files []string
		for _, f := range c.Files {
			files = append(files, core.Rel(f, c.Worktree))
		}
		m.field(&b, "files", strings.Join(files, "\n"))
	}
	section := func(name, text string) {
		if strings.TrimSpace(text) == "" {
			return
		}
		b.WriteString("\n" + pal.bold.Render(" "+name) + "\n")
		for _, l := range strings.Split(ansi.Wrap(strings.TrimSpace(text), max(20, m.width-4), " "), "\n") {
			b.WriteString("   " + l + "\n")
		}
	}
	section("BRIEF", c.Brief)
	section("RESULT", c.Result)
	if len(c.Trail) > 0 {
		b.WriteString("\n" + pal.bold.Render(fmt.Sprintf(" TRAIL (last %d actions)", len(c.Trail))) + "\n")
		for _, a := range c.Trail {
			at := ""
			if !a.At.IsZero() {
				at = a.At.Local().Format("15:04:05")
			}
			b.WriteString(pal.dim.Render("   "+pad(at, 9)) + a.String() + "\n")
		}
	}
	return b
}

func toolCounts(tools map[string]int) string {
	type tc struct {
		name string
		n    int
	}
	var all []tc
	for k, v := range tools {
		all = append(all, tc{k, v})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].n != all[j].n {
			return all[i].n > all[j].n
		}
		return all[i].name < all[j].name
	})
	var parts []string
	for _, t := range all {
		parts = append(parts, fmt.Sprintf("%s %d", t.name, t.n))
	}
	return strings.Join(parts, ", ")
}

// unprepared says what an ask that reached the owner lacks, and who was told to prepare it.
func (m Model) unprepared(it core.Item) string {
	missing := core.Unprepared(it)
	if len(missing) == 0 {
		return ""
	}
	out := "unprepared: missing " + strings.Join(missing, " and ")
	if mg, ok := m.board.Org.Managers[it.Preparer]; ok {
		out += "; " + mg.Label() + " was told to prepare it"
	}
	return out
}

// evidenceLine sums up an item's evidence in one line: its goal, pull requests with CI, and what changed since the owner looked.
func evidenceLine(it core.Item) string {
	var parts []string
	d := it.Dossier
	if d.Goal != "" {
		parts = append(parts, d.Goal)
	}
	if len(it.Evidence) > 0 {
		parts = append(parts, fmt.Sprintf("%d cited", len(it.Evidence)))
	}
	if n := len(d.PRs); n > 0 {
		red := 0
		for _, pr := range d.PRs {
			if pr.CI == "failing" {
				red++
			}
		}
		pr := fmt.Sprintf("%d PR", n)
		if n > 1 {
			pr += "s"
		}
		if red > 0 {
			pr += fmt.Sprintf(" (%d ci failing)", red)
		}
		parts = append(parts, pr)
	}
	if n := len(d.Changes); n > 0 {
		parts = append(parts, fmt.Sprintf("%d changes since you looked", n))
	}
	if it.Tried != "" {
		parts = append(parts, "tried: "+it.Tried)
	}
	if len(parts) == 0 {
		return ""
	}
	return "evidence: " + strings.Join(parts, " | ") + "   enter shows it"
}

func delivered(it core.Item) string {
	if !it.Held.IsZero() {
		return "  (held in session)"
	}
	if it.Delivered.IsZero() {
		return "  (not yet received)"
	}
	return "  (received)"
}

func (m Model) whyView() string {
	it := m.why
	var b strings.Builder
	b.WriteString(pal.bold.Render(" "+it.ID+"  "+it.Kind+"  "+it.State) + "\n " + it.Question + "\n\n")
	if it.Action == "chat" {
		b.WriteString(pal.yellow.Render("Action in the originating chat · o opens it, O opens beside") + "\n\n")
	}
	field := func(k, v string) {
		if v != "" {
			b.WriteString(" " + pal.bold.Render(k) + "  " + v + "\n")
		}
	}
	field("rec", it.Rec)
	for _, o := range it.Options {
		field("option", o.Label+" = "+o.Effect)
	}
	field("assumption", it.Assumption)
	if it.State == core.ItemOpen && it.Recipient == core.Owner {
		field("unprepared", strings.TrimPrefix(m.unprepared(*it), "unprepared: "))
	}
	field("tried", it.Tried)
	for _, ev := range it.Evidence {
		field("evidence", ev)
	}
	field("blocks", fmt.Sprint(it.Blocks))
	field("source", m.sourceName(*it)+"  "+it.Session)
	field("filed", it.CreatedAt.Local().Format("2006-01-02 15:04"))
	field("answer", it.Answer)
	field("dismissed", it.Reason)
	if !it.Dossier.Empty() {
		b.WriteString("\n" + pal.bold.Render(" CONTEXT") + pal.dim.Render("  attached by ym "+age(m.board.At, it.Dossier.At)) + "\n")
		last := ""
		for _, l := range core.DossierLines(it.Dossier) {
			k := l[0]
			if k == last {
				k = ""
			}
			last = l[0]
			b.WriteString(" " + pal.bold.Render(pad(k, 22)) + " " + l[1] + "\n")
		}
	}
	if len(it.History) > 1 {
		b.WriteString("\n" + pal.bold.Render(" history") + "\n")
		for _, e := range it.History[1:] {
			line := "   " + e.At.Local().Format("01-02 15:04") + "  " + e.From + " -> " + e.To
			if e.Text != "" {
				line += ": " + e.Text
			}
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

func (m Model) pickView() string {
	var b strings.Builder
	b.WriteString(pal.red.Render(" This session runs in more than one process. Pick one to open.") + "\n\n")
	for i, t := range m.targets {
		mark := "   "
		if i == m.cursor2 {
			mark = " > "
		}
		b.WriteString(mark + t.String() + "\n")
	}
	return b.String()
}

const Help = ` ym runs your work like a company: you hand a goal to a manager, the manager runs agents for it, and
 only what truly needs you reaches you.

 NEEDS YOU is your queue: stuck goals, escalations no manager or chief could settle, pull requests ready
 to merge in repos where managers may not merge, reviews GitHub asks of you personally, Notion discussions
 and reviews addressed to you that no goal claims, permission prompts, and one row per session you started
 that asks you something. A row leaves on its own once it is settled.
 GOALS groups the goals that report to you by repository, and lists every chief under "chiefs" with its
 managers and chiefs indented beneath it; a chief's standing is its reports' worst. Every row shows its standing and its brief's age, "brief stale" or "no brief"; enter on a
 chief opens it with its reports.
 A goal stands moving, quiet (nothing moved for a while; ym nudges its manager), adrift (an agent is busy
 going nowhere: the same failure again and again, its context compacted over and over, or writes in
 another repository; ym tells its manager the evidence), down (its manager's
 session died; ym relaunches it), stuck (nudges did not help, or it cannot launch), ym off (z turned ym off for it;
 ym leaves it alone), or done. A done goal stays
 on the tree with its summary for a day after it passed verification; x clears it sooner.
 SESSIONS YOU STARTED groups the sessions no manager runs by repository. enter drills down:
 repo -> missions -> sessions and crew -> session detail -> agent brief, result and trail. esc goes up.
 Agents a manager starts report to it: their ym item add reaches the manager's session, not you.

   Move
   ↑↓ or k j    move
   tab          next section: needs you, goals, sessions (shift-tab: previous)
   [ ]          previous or next repository group, across goals and sessions
   < > or home end
                top or bottom of the list; in an agent's page, its top or bottom
   enter        an item: expand it; a permission prompt: go to its session; an exited launch: revive it;
                anything else: open it
   esc          go back up, or clear the filter
   /            filter
   q            back or quit

   Act on the selected row
   r            reply: answer an item (an idle agent in tmux gets it typed in, others on their next turn);
                on a goal or chief, send it your message (every selected one); on a session, send it a follow-up
   g            go ahead: answer a go item with "PLEASE GO IMPLEMENT THIS"
   K            kill, after one y/n naming what dies (sessions, worktrees, unpushed commits, uncommitted
                files): on a session or mission, end its agent, close its tmux pane, remove the worktree ym
                made and drop the row; on a goal, the same for its manager, every agent under it and its
                verifier, the goal showing killed for a day; on a chief, retire it alone, its reports
                reporting one level up. Work no remote holds is kept in ym's state; ym revive <id> brings
                the session or goal back with it. Teardown runs in the background, its rows marked x.
                An exited launch with nothing to lose lands by itself
   c            chief menu: a attaches the goal or chief (and every selected one) to a chief you pick, or to
                you; d detaches it from its chief, so it reports to you (asks y/n); n starts a new chief:
                check the goals and chiefs it oversees with space (the selected ones start checked), enter,
                then type its name and its remit in your words (enter leaves it empty)
   b            show the goal's or chief's brief, the newest version its manager recorded
   n            new goal: pick its repository first, then type the goal in your words; a manager starts on it
   o            go to the selected row's session: jump to its tmux pane, or say where it runs, and mark
                what it said read;
                a session in another terminal app (iTerm, Terminal) is adopted: once idle, ym ends
                it there and resumes it on its own server
   O            open the selected row's session in a new tmux pane beside ym, so the board stays in
                view; needs ym running inside tmux
   x            dismiss the item with a reason; on a stuck goal, a merge, a review or a doc ask, clear it from the queue; on a done goal, clear it
                from the goal tree
   l            later: snooze the item
   u            reopen a snoozed item (detail)

   More
   N            new goal in the selected row's repository
   +            new session in the selected row's repository, run by you, no manager: type the prompt;
                the worktree is named from it
                n, N, + and c n: tab switches claude and codex, shift-tab the login profile, and each starts
                on the harness and host the last one used; ctrl+t picks this one or a remembered host
   space        select a goal or chief row; r, s and c then act on every selected one
   s            send: like r on a goal, chief or session; tab widens a chief's message to its whole subtree
   S            on a goal or chief: edit what it is about in $EDITOR; saving makes the next numbered
                revision, which shows "steer rN not adopted" until its manager runs ym goal adopt;
                on a mission: split a merged mission back out
   W            open the goal's or chief's brief page that ym serve hosts; a chief's rolls up its reports
   V            open the artifact a manager registered as its richer brief
   R            relaunch a manager or chief: end its session and start fresh from its prompt and brief
   z            ym off or back on: on a goal or chief, for its manager and every agent under it; on a
                session or mission, for that session. While off, ym delivers nothing to it and its ym
                commands refuse, so you talk to it in its own chat; ym on hands it what waited, in one message
   C            new chief, the same as c n
   D            detach the goal or chief from its chief, the same as c d
   t            goals by repository with chiefs nesting their reports, or every goal by its own repository
   e            rename a mission; an empty name restores the inferred one
   m            merge a mission into another
   p            advance the mission to its next phase (asks y/n)
   P            set the mission's phases, comma separated
   v            set the mission's vibe, any fitting place from Afghanistan (broken) to Hawaii (great): "place: why"
   G            gc: remove worktrees of known repos idle 3 days that are clean, on a remote and unused,
                then local branches merged into origin that no worktree uses (asks y/n); the board
                runs it by itself at most hourly
   T            end the idle session and relaunch it in the other harness on ym's server (asks y/n)
   B            switch the session's login profile (subscription)
   space f pgdown, b pgup
                page down or up in an agent's page or a text page
   I            every item, answered, dismissed and snoozed too; in a mission, just its items
   h            health: agents adrift now, weekly item answer times, stale items, questions reprinted in chat, time blocked on you
   U            usage: spend today and this week, limits, and the top sessions by spend
   E            every session ym launched and whether its agent still runs
   A            hide or show the mountains
   ?            this page
   ctrl+c       quit

 A mission's ≈ shares are its part of each current limit window. They count only this host's
 usage, so work on other hosts makes them run high.
 Colors: red needs you or fails, yellow waits, green runs or passes, faint is stale or exited;
 each project keeps one color of its own. NO_COLOR turns color off.
`

func pad(s string, n int) string {
	if w := ansi.StringWidth(s); w > n {
		return ansi.Truncate(s, n, "…")
	} else {
		return s + strings.Repeat(" ", n-w)
	}
}

func age(now, t time.Time) string {
	if t.IsZero() {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// goVerb is what going to the session does: adopt one in a terminal ym cannot reach, else go there.
func goVerb(s core.Live) string {
	if s.Adoptable() {
		return "adopt"
	}
	return "go to its session"
}
