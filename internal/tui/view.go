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
		b.WriteString(help)
	case modeWhy:
		b.WriteString(m.whyView())
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
	foot := m.footer()
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
	return h
}

func (m Model) footer() []string {
	var lines []string
	if m.mode == modeInput {
		label := map[inputKind]string{inAnswer: "answer " + m.inItem, inDismiss: "dismiss " + m.inItem + " because", inRename: "rename to", inFilter: "filter"}[m.inKind]
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
	case modeHelp, modeWhy:
		return "esc back"
	case modePick:
		return "↑↓ choose   enter open   esc back"
	case modeMerge:
		return "↑↓ choose mission   enter merge into it   esc cancel"
	case modeDetail:
		return "↑↓ move   enter open   a answer   l later   u unsnooze   w why   x dismiss   r rename   esc back   ? help"
	}
	return "↑↓ move   enter open   a answer   l later   w why   x dismiss   / filter   ? help   q quit"
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
	name := pad(m.projectName(n.Project), 28)
	sel := i == m.cursor
	var b strings.Builder
	if n.Item == nil {
		what := "permission prompt"
		if n.Session.Prompt != nil {
			what = n.Session.Prompt.String()
		}
		b.WriteString(m.mark(i) + alarm.Render("PERM ") + " " + name + " " + what + "\n")
		if sel {
			b.WriteString(dim.Render("         enter open its terminal") + "\n")
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
		b.WriteString(dim.Render("         a answer   l later   w why   x dismiss   enter open source") + "\n")
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
	phase := "------"
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
	crew := fmt.Sprintf("crew %d", p.Crew())
	where := ""
	if len(p.Sessions) > 0 {
		where = p.Sessions[0].Location()
		if len(p.Sessions) > 1 {
			where += fmt.Sprintf(" +%d", len(p.Sessions)-1)
		}
	}
	line := m.mark(i) + pad(p.Name, 30) + " " + pad(phase, 16) + " " + pad(crew, 9) + " " + pad(p.Status.String(), 8) + " " + where
	if p.Duplicated() {
		return line + "  " + alarm.Render(copiesText(p))
	}
	if p.Status != core.Waiting && i != m.cursor {
		return dim.Render(line)
	}
	return line
}

func copiesText(p core.Project) string {
	for _, s := range p.Sessions {
		if len(s.Copies) > 1 {
			var at []string
			for _, c := range s.Copies {
				at = append(at, fmt.Sprintf("pid %d %s", c.PID, c.TTY))
			}
			return fmt.Sprintf("%d COPIES (%s)", len(s.Copies), strings.Join(at, ", "))
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
		line := m.mark(i) + pad(title, 34) + " " + pad(s.Harness, 7) + pad(s.Status.String(), 9) + s.Location()
		if s.Prompt != nil {
			line += "  " + alarm.Render(s.Prompt.String())
		}
		if len(s.Copies) > 1 {
			line += "  " + alarm.Render(fmt.Sprintf("%d COPIES", len(s.Copies)))
		}
		b.WriteString(line + "\n")
		for _, c := range s.Crew {
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
   enter        open: a need's terminal, or a mission's detail
   o            open the terminal of the selected row
   a            answer an item; an idle agent gets it typed in, a busy one on its next turn
   l            later: snooze the item
   u            reopen a snoozed item (detail)
   w            why: the item's rec, options, assumption and history
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
