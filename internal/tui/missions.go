package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/progress"
)

// cols are the widths of a mission row's columns, shared by every row so each column starts at one x.
// A width of 0 drops the column: phase and bar when no mission has one, spend before usage is read, work when no mission has any, doing when the screen is too narrow.
type cols struct {
	indent, name, phase, state, bar, vibe, spend, doing int
	// work is the running crew, open pull requests and failing ones, each its own fixed width.
	work [3]int
}

const (
	phaseName = 8
	phaseDots = 7
	minName   = 14
	minDoing  = 20
)

// missionCols sizes the columns to the board; on a narrow screen limit shares give way to doing, then doing goes, then names shorten, then the work cell goes.
func (m Model) missionCols(indent int) cols {
	c := cols{indent: indent, state: 8, vibe: len("no vibe")}
	dollars := 0
	for _, p := range m.board.Projects {
		c.name = max(c.name, ansi.StringWidth(p.Name))
		if len(p.Phases.Names) > 0 {
			c.phase = phaseName + 1 + phaseDots
		}
		if _, ok := progress.Of(p).Fraction(); ok {
			c.bar = 18
		}
		if v, ok := p.Vibe(); ok {
			c.vibe = max(c.vibe, ansi.StringWidth(v.Place))
		}
		c.spend = max(c.spend, ansi.StringWidth(m.missionSpend(p)))
		dollars = max(dollars, ansi.StringWidth(m.dollars(p)))
		for j, part := range workParts(p) {
			c.work[j] = max(c.work[j], ansi.StringWidth(part))
		}
	}
	c.name = min(44, max(minName, c.name))
	c.vibe = min(16, c.vibe)
	c.spend = min(32, c.spend)
	room := m.width - 1
	if free := room - c.fixed() - 1; free >= minDoing {
		c.doing = free
	} else if c.spend-dollars >= minDoing-free {
		c.spend -= minDoing - free
		c.doing = minDoing
	}
	if over := c.fixed() - room; over > 0 && c.spend > 0 {
		c.spend = max(dollars, c.spend-over)
	}
	if over := c.fixed() - room; over > 0 {
		c.name = max(minName, c.name-over)
	}
	if c.fixed() > room {
		c.work = [3]int{}
	}
	return c
}

// fixed is every cell but doing's own width: the cursor mark, indent, columns and the space after each.
func (c cols) fixed() int {
	w := 3 + c.indent + c.name
	for _, n := range []int{c.phase, c.state, c.bar, c.vibe, c.spend, c.workWidth()} {
		if n > 0 {
			w += 1 + n
		}
	}
	if c.doing > 0 {
		w += 1 + c.doing
	}
	return w
}

func (c cols) workWidth() int {
	w := -1
	for _, n := range c.work {
		if n > 0 {
			w += 1 + n
		}
	}
	return max(0, w)
}

// missionLine is one mission: name, phase, state, progress, vibe, spend, then what its lead does now.
func (m Model) missionLine(i int, p core.Project, c cols) string {
	exited := len(p.Sessions) == 0 && len(p.Dormant) > 0
	name := m.hue(p.Key)
	if exited {
		name = pal.dim
	}
	cells := []string{name.Render(pad(p.Name, c.name))}
	if c.phase > 0 {
		cells = append(cells, phaseCell(p.Phases))
	}
	state := p.Status.String()
	if exited {
		state = "exited"
	}
	st := pal.state(state).Render(pad(state, c.state))
	if p.Duplicated() {
		st = pal.red.Render(pad(copiesText(p), c.state))
	}
	cells = append(cells, st)
	if c.bar > 0 {
		cells = append(cells, barCell(p, c.bar))
	}
	cells = append(cells, m.vibeCell(p, c.vibe))
	if c.spend > 0 {
		cells = append(cells, m.spendCell(p, c.spend))
	}
	if c.workWidth() > 0 {
		cells = append(cells, workCell(p, c.work))
	}
	if c.doing > 0 {
		switch {
		case p.Duplicated():
			cells = append(cells, pal.red.Render(pad(copiesWhere(p), c.doing)))
		case len(p.Sessions) > 0:
			cells = append(cells, doingCell(m.board.At, p.Sessions[0], c.doing))
		}
	}
	line := strings.TrimRight(m.mark(i)+strings.Repeat(" ", c.indent)+strings.Join(cells, " "), " ") + "\n"
	if p.Work != nil && i == m.cursor {
		w := p.Work
		assessment := w.Brief.Assessment
		if assessment == "" {
			assessment = "brief not published"
		} else if !w.BriefCurrent() {
			assessment = "assessment needs refresh · " + assessment
		}
		line += pal.dim.Render("     "+pad(assessment, max(10, m.width-6))) + "\n"
		line += pal.dim.Render(fmt.Sprintf("     %d decisions ready · B brief · C context", w.Ready())) + "\n"
	}
	return line
}

// hue is the project's color on this board, apart from every other project's while there are colors to spare.
func (m Model) hue(key string) lipgloss.Style {
	if i, ok := m.alpine.hues[key]; ok {
		return pal.hues[i]
	}
	keys := make([]string, len(m.board.Projects))
	for i, p := range m.board.Projects {
		keys[i] = p.Key
	}
	if i, ok := hueSlots(keys)[key]; ok {
		return pal.hues[i]
	}
	return pal.hue(key)
}

// phaseCell is the current phase's name and one dot per phase, each part clipped to its own width, blank without phases.
func phaseCell(ph core.Phases) string {
	if len(ph.Names) == 0 {
		return pad("", phaseName+1+phaseDots)
	}
	var dots []string
	for j := range ph.Names {
		if j <= ph.Current {
			dots = append(dots, "●")
		} else {
			dots = append(dots, "○")
		}
	}
	if len(dots) > phaseDots {
		dots = append(dots[:phaseDots-1], "…")
	}
	return pad(ph.Names[ph.Current], phaseName) + " " + pad(strings.Join(dots, ""), phaseDots)
}

// progressLegend says what a mission row's done percent counts.
var progressLegend = "done = agents finished + PR steps cleared (" + strings.Join(progress.Gates[:], "→") + ")"

func barCell(p core.Project, w int) string {
	if f, ok := progress.Of(p).Fraction(); ok {
		return pad(progress.Bar(f, 8)+fmt.Sprintf(" %3.0f%% done", f*100), w)
	}
	return pad("", w)
}

// vibeCell is a mission's latest vibe, faint once stale, or a faint "no vibe".
func (m Model) vibeCell(p core.Project, w int) string {
	v, ok := p.Vibe()
	if !ok {
		return pal.dim.Render(pad("no vibe", w))
	}
	if p.VibeStale(m.board.At) {
		return pal.dim.Render(pad(v.Place, w))
	}
	return pad(v.Place, w)
}

// dollars is a mission's spend without its limit shares, or "" until usage is read.
func (m Model) dollars(p core.Project) string {
	if !m.board.Fleet.Ready {
		return ""
	}
	return p.Spend.Dollars()
}

// spendCell is the spend and as many whole limit shares as fit in w.
func (m Model) spendCell(p core.Project, w int) string {
	if full := m.missionSpend(p); ansi.StringWidth(full) <= w {
		return pad(full, w)
	}
	text, shares := m.dollars(p), ""
	for _, s := range strings.Split(m.board.Fleet.Shares(p.Spend), " · ") {
		next := s
		if shares != "" {
			next = shares + " · " + s
		}
		if ansi.StringWidth(text+" "+next) > w {
			break
		}
		shares = next
	}
	if shares != "" {
		text += " " + pal.dim.Render(shares)
	}
	return pad(text, w)
}

// workParts are a mission's running crew, open pull requests and failing ones, each blank when there are none.
func workParts(p core.Project) [3]string {
	var parts [3]string
	if n := runningCrew(p.Sessions); n > 0 {
		parts[0] = fmt.Sprintf("%d▶", n)
	}
	t := tallyPRs(prsOf(p.Sessions))
	if t.open > 0 {
		parts[1] = fmt.Sprintf("%dPR", t.open)
	}
	if t.fail > 0 {
		parts[2] = fmt.Sprintf("✗%d", t.fail)
	}
	return parts
}

// workCell lines each work part up under the same part of every other row.
func workCell(p core.Project, w [3]int) string {
	styles := [3]lipgloss.Style{pal.green, pal.plain, pal.red}
	var cells []string
	for j, part := range workParts(p) {
		if w[j] > 0 {
			cells = append(cells, styles[j].Render(pad(part, w[j])))
		}
	}
	return strings.Join(cells, " ")
}

// repoLine is a repo's name and its counts.
func (m Model) repoLine(i int, r core.Repo, c cols) string {
	return m.mark(i) + pal.bold.Render(pad(r.Name, c.indent+c.name)) + " " + m.repoCounts(r) + "\n"
}

// repoCounts sums a repo's missions: open needs first so a narrow screen keeps them, then sessions by status, running crew and pull requests.
func (m Model) repoCounts(r core.Repo) string {
	var sessions []core.Live
	exited := 0
	for _, p := range r.Missions {
		sessions = append(sessions, p.Sessions...)
		exited += len(p.Dormant)
	}
	parts := []string{plural(len(r.Missions), "mission")}
	if n := m.needsIn(r); n > 0 {
		parts = append(parts, pal.red.Render(fmt.Sprintf("%d need you", n)))
	}
	parts = append(parts, sessionCounts(sessions))
	if exited > 0 {
		parts = append(parts, fmt.Sprintf("%d exited", exited))
	}
	if run := runningCrew(sessions); run > 0 {
		parts = append(parts, plural(run, "agent")+" running")
	}
	if pr := prSummary(prsOf(sessions)); pr != "" {
		parts = append(parts, pr)
	}
	return strings.Join(parts, " · ")
}
