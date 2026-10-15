package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

var (
	bold  = lipgloss.NewStyle().Bold(true)
	dim   = lipgloss.NewStyle().Faint(true)
	loud  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3"))
	alarm = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("1"))
)

func (m Model) View() string {
	var b strings.Builder
	b.WriteString(m.header() + "\n\n")
	switch m.mode {
	case modeHelp:
		b.WriteString(ansi.Wrap(help, m.width, " "))
	case modeWhy:
		b.WriteString(ansi.Wrap(m.whyView(), m.width, " "))
	case modePick:
		b.WriteString(m.pickView())
	default:
		if m.listMode() == modeDetail {
			b.WriteString(m.detailView())
		} else {
			b.WriteString(m.listView())
		}
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	var foot []string
	for _, l := range m.footer() {
		foot = append(foot, strings.Split(ansi.Wrap(l, max(20, m.width-1), " "), "\n")...)
	}
	room := max(1, m.height-len(foot))
	if len(lines) > room {
		lines = m.scroll(lines, room)
	}
	for len(lines) < room {
		lines = append(lines, "")
	}
	lines = append(lines, foot...)
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, m.width, "…")
	}
	return strings.Join(lines, "\n")
}

// scroll keeps the cursor's line on screen when the list outgrows it.
func (m Model) scroll(lines []string, room int) []string {
	at := 0
	for i, l := range lines {
		if strings.HasPrefix(ansi.Strip(l), " >") {
			at = i
		}
	}
	start := max(0, min(at-room/3, len(lines)-room))
	return lines[start : start+room]
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
	parts := []string{fmt.Sprintf("%d sessions", len(m.board.Sessions))}
	for _, st := range []core.Status{core.Busy, core.Waiting, core.Idle, core.Unknown} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	h := bold.Render(" ym") + "    " + strings.Join(parts, "  ")
	if m.err != nil {
		h += "  " + alarm.Render("refresh failed: "+m.err.Error())
	}
	if m.core != nil && m.core.Meter != nil {
		h += "\n" + fleetLine(m.board.Fleet, m.board.At)
	}
	return h
}

func (m Model) footer() []string {
	var lines []string
	if m.mode == modeInput {
		label := map[inputKind]string{inAnswer: "answer " + m.inItem, inDismiss: "dismiss " + m.inItem + " because", inRename: "rename to", inFilter: "filter"}[m.inKind]
		if m.fresh {
			lines = append(lines, " "+bold.Render(label+":")+" "+dim.Render(string(m.input))+"█")
			lines = append(lines, dim.Render(" enter accepts the default   type to replace it   esc cancel"))
			return lines
		}
		lines = append(lines, " "+bold.Render(label+":")+" "+string(m.input)+"█")
		lines = append(lines, dim.Render(" enter save   esc cancel   ctrl+u clear"))
		return lines
	}
	if m.message != "" {
		lines = append(lines, " "+m.message)
	}
	if m.filter != "" {
		lines = append(lines, dim.Render(" filter: "+m.filter+"   esc clears"))
	}
	lines = append(lines, dim.Render(" "+m.hints()))
	return lines
}

func (m Model) hints() string {
	switch m.mode {
	case modeHelp:
		return "esc back"
	case modeWhy:
		return "a answer   l later   x dismiss   u unsnooze   o go to its session   esc back"
	case modePick:
		return "↑↓ choose   enter open   esc back"
	case modeMerge:
		return "↑↓ choose mission   enter merge into it   esc cancel"
	case modeDetail:
		return "↑↓ move   enter expand or go to session   o go to session   a answer   l later   u unsnooze   x dismiss   r rename   esc back   ? help"
	}
	return "↑↓ move   enter expand   o go to session   a answer   l later   x dismiss   / filter   ? help   q quit"
}

func (m Model) mark(i int) string {
	if i == m.cursor {
		return " > "
	}
	return "   "
}

func (m Model) listView() string {
	if !m.loaded {
		return ""
	}
	var b strings.Builder
	rows := m.rows()
	needs := 0
	for _, r := range rows {
		if r.need != nil {
			needs++
		}
	}
	b.WriteString(bold.Render(fmt.Sprintf(" NEEDS YOU (%d)", needs)) + "\n")
	if needs == 0 {
		b.WriteString(dim.Render("   nothing") + "\n")
	}
	missions := false
	for i, r := range rows {
		if r.need == nil && !missions {
			missions = true
			b.WriteString("\n" + bold.Render(" MISSIONS") + "\n")
			b.WriteString(dim.Render("   "+pad("", m.nameWidth())+" "+m.phaseCol("phase")+pad("crew", 16)+" "+pad("status", 8)+" "+m.spendCol("spend")+"where") + "\n")
		}
		if r.need != nil {
			b.WriteString(m.needLines(i, *r.need))
		} else {
			b.WriteString(m.missionLine(i, *r.project) + "\n")
		}
	}
	if !missions {
		b.WriteString("\n" + bold.Render(" MISSIONS") + "\n" + dim.Render("   no sessions found") + "\n")
	}
	return b.String()
}

func (m Model) needLines(i int, n core.Need) string {
	name := pad(m.projectName(n.Project), min(32, max(16, m.width/4)))
	sel := i == m.cursor
	var b strings.Builder
	if n.Item == nil {
		what := "permission prompt"
		if n.Session.Prompt != nil {
			what = n.Session.Prompt.String()
		}
		b.WriteString(m.mark(i) + alarm.Render("PERM ") + " " + name + " " + what + "\n")
		if sel {
			b.WriteString(dim.Render("         enter go to its session") + "\n")
		}
		return b.String()
	}
	it := n.Item
	b.WriteString(m.mark(i) + loud.Render(pad(it.ID, 5)) + " " + name + " " + it.Question + "\n")
	var meta []string
	if it.Rec != "" {
		meta = append(meta, "rec: "+it.Rec)
	}
	meta = append(meta, fmt.Sprintf("blocks %d", it.Blocks), "src: "+m.sourceName(*it), age(m.board.At, it.CreatedAt))
	b.WriteString(dim.Render("         "+strings.Join(meta, " | ")) + "\n")
	if sel {
		b.WriteString(dim.Render("         enter expand   a answer   l later   x dismiss   o go to session") + "\n")
	}
	return b.String()
}

func (m Model) sourceName(it core.Item) string {
	if s, ok := m.board.Session(it.Session); ok && s.Title != "" {
		return s.Title
	}
	return it.Harness + " " + core.Short(it.Session)
}

func (m Model) missionLine(i int, p core.Project) string {
	phase := ""
	if len(p.Phases.Names) > 0 {
		dots := ""
		for j := range p.Phases.Names {
			if j <= p.Phases.Current {
				dots += "●"
			} else {
				dots += "○"
			}
		}
		phase = p.Phases.Names[p.Phases.Current] + " " + dots
	}
	crew := crewText(p)
	where := ""
	if len(p.Sessions) > 0 {
		where = p.Sessions[0].Location()
		if len(p.Sessions) > 1 {
			where += fmt.Sprintf(" +%d", len(p.Sessions)-1)
		}
	}
	status := pad(p.Status.String(), 8)
	if p.Duplicated() {
		status = alarm.Render(pad(copiesText(p), 8))
	}
	line := m.mark(i) + pad(p.Name, m.nameWidth()) + " " + m.phaseCol(phase) + pad(crew, 16) + " " + status + " " + m.spendCol(p.Spend.Dollars()) + where
	if p.Duplicated() {
		return line + "  " + alarm.Render(copiesWhere(p))
	}
	if p.Status != core.Waiting && i != m.cursor {
		return dim.Render(line)
	}
	return line
}

// nameWidth gives mission names what the fixed columns leave, between 20 and 60 cells.
func (m Model) nameWidth() int {
	return min(60, max(20, m.width-3-len(m.phaseCol(""))-len(m.spendCol(""))-16-8-16-3))
}

// phaseCol is a phase cell, or nothing when no mission has phases.
func (m Model) phaseCol(s string) string {
	for _, p := range m.board.Projects {
		if len(p.Phases.Names) > 0 {
			return pad(s, 12) + " "
		}
	}
	return ""
}

func crewCounts(p core.Project) (running, idle, done int) {
	for _, s := range p.Sessions {
		for _, c := range s.Crew {
			switch c.Status {
			case core.Busy:
				running++
			case core.Done:
				done++
			default:
				idle++
			}
		}
	}
	return
}

func crewText(p core.Project) string {
	running, idle, _ := crewCounts(p)
	var parts []string
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d run", running))
	}
	if idle > 0 {
		parts = append(parts, fmt.Sprintf("%d idle", idle))
	}
	return strings.Join(parts, " · ")
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

func (m Model) detailView() string {
	p, ok := m.board.Project(m.detail)
	if !ok {
		return dim.Render("   this mission has no live sessions now; esc back")
	}
	var b strings.Builder
	b.WriteString(bold.Render(" "+p.Name) + dim.Render("  "+p.Key) + "\n")
	if len(p.Phases.Names) > 0 {
		var ps []string
		for j, name := range p.Phases.Names {
			switch {
			case j < p.Phases.Current:
				ps = append(ps, dim.Render("✓ "+name))
			case j == p.Phases.Current:
				ps = append(ps, bold.Render("▶ "+name))
			default:
				ps = append(ps, "○ "+name)
			}
		}
		b.WriteString(" phases  " + strings.Join(ps, "  ") + "\n")
	}
	b.WriteString("\n" + bold.Render(" SESSIONS") + "\n")
	i := 0
	for _, s := range p.Sessions {
		title := s.Title
		if title == "" {
			title = core.Short(s.Key())
		}
		line := m.mark(i) + pad(title, m.nameWidth()) + " " + pad(s.Harness, 7) + pad(s.Status.String(), 9) + s.Location()
		if s.Prompt != nil {
			line += "  " + alarm.Render(s.Prompt.String())
		}
		if len(s.Copies) > 1 {
			line += "  " + alarm.Render(fmt.Sprintf("%d COPIES", len(s.Copies)))
		}
		b.WriteString(line + "\n")
		done := 0
		for _, c := range s.Crew {
			if c.Status == core.Done {
				done++
				continue
			}
			kind := "subagent"
			if c.Teammate {
				kind = "teammate"
			}
			l := "       " + pad(c.Name, 40) + " " + pad(kind, 9) + c.Status.String()
			if c.Status != core.Busy {
				l = dim.Render(l)
			}
			b.WriteString(l + "\n")
		}
		if done > 0 {
			b.WriteString(dim.Render(fmt.Sprintf("       +%d finished subagents", done)) + "\n")
		}
		i++
	}
	b.WriteString("\n" + bold.Render(" ITEMS") + "\n")
	if len(p.Items) == 0 {
		b.WriteString(dim.Render("   none") + "\n")
	}
	for _, it := range p.Items {
		line := m.mark(i) + pad(it.ID, 5) + " " + pad(it.State, 10) + " " + it.Question
		if it.State != core.ItemOpen && i != m.cursor {
			line = dim.Render(line)
		}
		b.WriteString(line + "\n")
		if it.Answer != "" {
			b.WriteString(dim.Render("         answer: "+it.Answer+delivered(it)) + "\n")
		}
		if it.Reason != "" {
			b.WriteString(dim.Render("         dismissed: "+it.Reason+delivered(it)) + "\n")
		}
		i++
	}
	return b.String()
}

func delivered(it core.Item) string {
	if it.Delivered.IsZero() {
		return "  (not yet received)"
	}
	return "  (received)"
}

func (m Model) whyView() string {
	it := m.why
	var b strings.Builder
	b.WriteString(bold.Render(" "+it.ID+"  "+it.Kind+"  "+it.State) + "\n " + it.Question + "\n\n")
	field := func(k, v string) {
		if v != "" {
			b.WriteString(" " + bold.Render(k) + "  " + v + "\n")
		}
	}
	field("rec", it.Rec)
	for _, o := range it.Options {
		field("option", o.Label+" = "+o.Effect)
	}
	field("assumption", it.Assumption)
	field("blocks", fmt.Sprint(it.Blocks))
	field("source", m.sourceName(*it)+"  "+it.Session)
	field("filed", it.CreatedAt.Local().Format("2006-01-02 15:04"))
	field("answer", it.Answer)
	field("dismissed", it.Reason)
	if len(it.History) > 1 {
		b.WriteString("\n" + bold.Render(" history") + "\n")
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
	b.WriteString(alarm.Render(" This session runs in more than one process. Pick one to open.") + "\n\n")
	for i, t := range m.targets {
		mark := "   "
		if i == m.cursor2 {
			mark = " > "
		}
		b.WriteString(mark + t.String() + "\n")
	}
	return b.String()
}

const help = ` ym shows every Claude Code and Codex session and what needs you.

 NEEDS YOU lists permission prompts first, then open items, most blocking first.
 MISSIONS groups each session with the sessions it spawned; enter shows a mission's detail.

   ↑ ↓ or k j   move
   enter        an item: expand it; a permission prompt: go to its session; a mission: its detail
   o            go to the selected row's session: jump to its tmux pane, or say where it runs
   a            answer an item; an idle agent in tmux gets it typed in, others on their next turn
   l            later: snooze the item
   u            reopen a snoozed item (detail)
   x            dismiss the item with a reason
   r            rename a mission
   m            merge a mission into another
   s            split a merged mission back out
   /            filter
   q            back or quit
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
