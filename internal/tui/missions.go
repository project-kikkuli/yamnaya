package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/progress"
)

// cols are the widths of a mission row's columns, shared by every row so each column starts at one x.
// A width of 0 drops the column: phase and bar when no mission has one, spend before usage is read, doing when the screen is too narrow.
type cols struct {
	indent, name, phase, state, bar, vibe, spend, doing int
}

const (
	phaseName = 8
	phaseDots = 7
	minName   = 14
	minDoing  = 12
)

// missionCols sizes the columns to the board: names get their longest, doing what is left; a narrow screen drops doing first, then shortens names, then limit shares.
func (m Model) missionCols(indent int) cols {
	c := cols{indent: indent, state: 8, vibe: len("no vibe")}
	for _, p := range m.board.Projects {
		c.name = max(c.name, ansi.StringWidth(p.Name))
		if len(p.Phases.Names) > 0 {
			c.phase = phaseName + 1 + phaseDots
		}
		if _, ok := progress.Of(p).Fraction(); ok {
			c.bar = 13
		}
		if v, ok := p.Vibe(); ok {
			c.vibe = max(c.vibe, ansi.StringWidth(v.Place))
		}
		c.spend = max(c.spend, ansi.StringWidth(m.missionSpend(p)))
	}
	c.name = min(44, max(minName, c.name))
	c.vibe = min(16, c.vibe)
	c.spend = min(32, c.spend)
	c.doing = m.width - 1 - c.fixed()
	if c.doing < minDoing {
		c.doing = 0
		c.name = max(minName, c.name-(c.fixed()-(m.width-1)))
	}
	if over := c.fixed() - (m.width - 1); over > 0 && c.spend > 0 {
		c.spend = max(len("$0.00"), c.spend-over)
	}
	return c
}

// fixed is every cell but doing's: the cursor mark, indent, columns and the space after each.
func (c cols) fixed() int {
	w := 3 + c.indent + c.name
	for _, n := range []int{c.phase, c.state, c.bar, c.vibe, c.spend} {
		if n > 0 {
			w += 1 + n
		}
	}
	if c.doing > 0 {
		w++
	}
	return w
}

// missionLine is one mission: name, phase, state, progress, vibe, spend, then what its lead does now.
func (m Model) missionLine(i int, p core.Project, c cols) string {
	exited := len(p.Sessions) == 0 && len(p.Dormant) > 0
	name := pal.hue(p.Key)
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
		cells = append(cells, pad(m.missionSpend(p), c.spend))
	}
	if c.doing > 0 {
		switch {
		case p.Duplicated():
			cells = append(cells, pal.red.Render(pad(copiesWhere(p), c.doing)))
		case len(p.Sessions) > 0:
			cells = append(cells, doingCell(m.board.At, p.Sessions[0], c.doing))
		}
	}
	return strings.TrimRight(m.mark(i)+strings.Repeat(" ", c.indent)+strings.Join(cells, " "), " ") + "\n"
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

func barCell(p core.Project, w int) string {
	if f, ok := progress.Of(p).Fraction(); ok {
		return pad(progress.Bar(f, 8)+fmt.Sprintf(" %3.0f%%", f*100), w)
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

// repoLine is a repo's name and its counts.
func (m Model) repoLine(i int, r core.Repo, c cols) string {
	return m.mark(i) + pal.bold.Render(pad(r.Name, c.indent+c.name)) + " " + m.repoCounts(r) + "\n"
}

// repoCounts sums a repo's missions: sessions by status, running crew, pull requests and open needs.
func (m Model) repoCounts(r core.Repo) string {
	var sessions []core.Live
	exited := 0
	for _, p := range r.Missions {
		sessions = append(sessions, p.Sessions...)
		exited += len(p.Dormant)
	}
	parts := []string{plural(len(r.Missions), "mission"), sessionCounts(sessions)}
	if exited > 0 {
		parts = append(parts, fmt.Sprintf("%d exited", exited))
	}
	if run := runningCrew(sessions); run > 0 {
		parts = append(parts, plural(run, "agent")+" running")
	}
	if pr := prSummary(prsOf(sessions)); pr != "" {
		parts = append(parts, pr)
	}
	if n := m.needsIn(r); n > 0 {
		parts = append(parts, pal.red.Render(fmt.Sprintf("%d need you", n)))
	}
	return strings.Join(parts, " · ")
}
