package tui

import (
	"fmt"
	"sort"
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
		switch m.listMode() {
		case modeRepo:
			b.WriteString(m.repoView())
		case modeDetail:
			b.WriteString(m.detailView())
		case modeSession:
			b.WriteString(m.sessionView())
		case modeAgent:
			b.WriteString(m.agentView())
		default:
			b.WriteString(m.listView())
		}
	}
	lines := strings.Split(strings.TrimRight(b.String(), "\n"), "\n")
	var foot []string
	for _, l := range m.footer() {
		foot = append(foot, strings.Split(ansi.Wrap(l, max(20, m.width-1), " "), "\n")...)
	}
	room := max(1, m.height-len(foot))
	if m.mode == modeAgent && len(lines) > room {
		start := min(m.scrollY, len(lines)-room)
		lines = append(lines[:2:2], lines[2+start:min(len(lines), 2+start+room-2)]...)
	} else if len(lines) > room {
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
	case modeRepo:
		return "↑↓ move   enter open mission   o go to session   r rename   m merge   s split   esc back   ? help"
	case modeDetail:
		return "↑↓ move   enter open session, agent or item   o go to session   a answer   l later   u unsnooze   x dismiss   r rename   esc back   ? help"
	case modeSession:
		return "↑↓ move   enter open agent or item   o go to session   a answer   x dismiss   esc back   ? help"
	case modeAgent:
		return "↑↓ scroll   space/b page   o go to its session   esc back"
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
	b.WriteString(bold.Render(" REPOS AND MISSIONS") + "\n")
	if len(rows) == needs {
		b.WriteString(dim.Render("   no sessions found") + "\n")
	}
	for i, r := range rows {
		if r.need != nil && i == len(rows)-needs {
			b.WriteString("\n" + bold.Render(fmt.Sprintf(" NEEDS YOU (%d)", needs)) + "\n")
		}
		switch {
		case r.need != nil:
			b.WriteString(m.needLines(i, *r.need))
		case r.project == nil:
			b.WriteString(m.repoLine(i, *r.repo))
		default:
			b.WriteString(m.missionLine(i, *r.project, "  "))
		}
	}
	if needs == 0 {
		b.WriteString("\n" + bold.Render(" NEEDS YOU (0)") + "\n" + dim.Render("   nothing") + "\n")
	}
	return b.String()
}

// repoLine sums a repo's missions: sessions by status, running crew, open needs and pull requests.
func (m Model) repoLine(i int, r core.Repo) string {
	var sessions []core.Live
	for _, p := range r.Missions {
		sessions = append(sessions, p.Sessions...)
	}
	parts := []string{plural(len(r.Missions), "mission"), sessionCounts(sessions)}
	if run := runningCrew(sessions); run > 0 {
		parts = append(parts, plural(run, "agent")+" running")
	}
	if n := m.needsIn(r); n > 0 {
		parts = append(parts, loud.Render(fmt.Sprintf("%d need you", n)))
	}
	if pr := prSummary(prsOf(sessions)); pr != "" {
		parts = append(parts, pr)
	}
	if sp := m.spend(sessions...); sp != "" {
		parts = append(parts, sp)
	}
	return m.mark(i) + bold.Render(pad(r.Name, min(28, max(12, m.width/5)))) + "  " + strings.Join(parts, " · ") + "\n"
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

// missionLine is one mission: name, phase, status, crew, pull requests, then what its lead does now.
func (m Model) missionLine(i int, p core.Project, indent string) string {
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
	status := pad(p.Status.String(), 8)
	if p.Duplicated() {
		status = alarm.Render(pad(copiesText(p), 8))
	}
	nw := m.nameWidth() - len(indent)
	line := m.mark(i) + indent + pad(p.Name, nw) + " " + m.phaseCol(phase) + status + " " + pad(crewText(p), 14) + " " + pad(prSummary(prsOf(p.Sessions)), 16)
	if sp := m.spend(p.Sessions...); sp != "" {
		line += " " + pad(sp, 8)
	}
	if len(p.Sessions) > 0 {
		line += " " + doingText(m.board.At, p.Sessions[0].Session)
	}
	if p.Duplicated() {
		return line + "  " + alarm.Render(copiesWhere(p)) + "\n"
	}
	if p.Status != core.Waiting && i != m.cursor {
		return dim.Render(line) + "\n"
	}
	return line + "\n"
}

// nameWidth gives mission names what the fixed columns leave, between 20 and 44 cells.
func (m Model) nameWidth() int {
	return min(44, max(20, m.width/3))
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
	if n := len(p.Sessions); n > 1 {
		parts = append(parts, fmt.Sprintf("%d sess", n))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("%d run", running))
	}
	if idle > 0 {
		parts = append(parts, fmt.Sprintf("%d idle", idle))
	}
	return strings.Join(parts, " · ")
}

func runningCrew(sessions []core.Live) int {
	n := 0
	for _, s := range sessions {
		for _, c := range s.Crew {
			if c.Status == core.Busy {
				n++
			}
		}
	}
	return n
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
func prSummary(prs []core.PR) string {
	var open, fail, run, pass, merged int
	for _, pr := range prs {
		switch {
		case pr.Status != nil && pr.Status.State == "MERGED":
			merged++
		case pr.Status != nil && pr.Status.State == "CLOSED":
		default:
			open++
			if pr.Status != nil {
				switch pr.Status.CI() {
				case "failing":
					fail++
				case "running":
					run++
				case "passing":
					pass++
				}
			}
		}
	}
	var parts []string
	if open > 0 {
		s := plural(open, "PR")
		var ci []string
		if fail > 0 {
			ci = append(ci, alarm.Render(fmt.Sprintf("✗%d", fail)))
		}
		if run > 0 {
			ci = append(ci, fmt.Sprintf("…%d", run))
		}
		if pass > 0 {
			ci = append(ci, fmt.Sprintf("✓%d", pass))
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
	return s.Doing.String() + dim.Render(" "+age(now, s.Doing.At))
}

func memberDoing(now time.Time, c core.Member) string {
	if c.Doing.Tool == "" {
		return ""
	}
	return c.Doing.String() + " " + age(now, c.Doing.At)
}

// spend is the usage column; it stays empty until a usage source is wired in.
func (m Model) spend(sessions ...core.Live) string { return "" }

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
	if sel {
		b.WriteString(dim.Render("         "+strings.Join(meta, " | ")) + "\n")
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

func (m Model) repoView() string {
	r, ok := m.board.Repo(m.repo)
	if !ok {
		return dim.Render("   this repo has no live sessions now; esc back")
	}
	var b strings.Builder
	key := r.Key
	if key == "" {
		key = "sessions outside any git repo"
	}
	b.WriteString(bold.Render(" "+r.Name) + dim.Render("  "+key) + "\n")
	b.WriteString(" " + strings.TrimSpace(ansi.Strip(m.repoLine(-1, r))[len(r.Name)+3:]) + "\n")
	b.WriteString("\n" + bold.Render(" MISSIONS") + "\n")
	for i, p := range r.Missions {
		b.WriteString(m.missionLine(i, p, ""))
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
			b.WriteString(dim.Render("     "+strings.Join(meta, " | ")) + "\n")
		}
	}
	return b.String()
}

func (m Model) detailView() string {
	p, ok := m.board.Project(m.detail)
	if !ok {
		return dim.Render("   this mission has no live sessions now; esc back")
	}
	var b strings.Builder
	b.WriteString(bold.Render(" "+p.Name) + dim.Render("  "+p.RepoName+"  "+p.Key) + "\n")
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
	if pr := prSummary(prsOf(p.Sessions)); pr != "" {
		b.WriteString(" prs     " + pr + "\n")
	}
	b.WriteString("\n" + bold.Render(" SESSIONS AND CREW") + "\n")
	i := 0
	for _, s := range p.Sessions {
		title := s.Title
		if title == "" {
			title = core.Short(s.Key())
		}
		line := m.mark(i) + pad(title, m.nameWidth()) + " " + pad(s.Harness, 7) + pad(s.Status.String(), 9) + pad(s.Location(), 18)
		if s.Branch != "" {
			line += " " + s.Branch
		}
		if sp := m.spend(s); sp != "" {
			line += "  " + sp
		}
		if s.Prompt != nil {
			line += "  " + alarm.Render(s.Prompt.String())
		}
		if len(s.Copies) > 1 {
			line += "  " + alarm.Render(fmt.Sprintf("%d COPIES", len(s.Copies)))
		}
		b.WriteString(line + "\n")
		if d := doingText(m.board.At, s.Session); d != "" {
			b.WriteString(dim.Render("     now: ") + d + "\n")
		}
		i++
		for _, c := range s.Crew {
			b.WriteString(m.crewLine(i, c))
			i++
		}
	}
	b.WriteString("\n" + bold.Render(" ITEMS") + "\n")
	if len(p.Items) == 0 {
		b.WriteString(dim.Render("   none") + "\n")
	}
	for _, it := range p.Items {
		b.WriteString(m.itemLines(i, it))
		i++
	}
	return b.String()
}

func (m Model) crewLine(i int, c core.Member) string {
	kind := c.Type
	if kind == "" {
		kind = "subagent"
		if c.Teammate {
			kind = "teammate"
		}
	}
	l := m.mark(i) + "  └ " + pad(c.Name, max(16, m.nameWidth()-4)) + " " + pad(kind, 16) + " " + pad(c.Status.String(), 8) + " " + pad(shortModel(c.Model), 10)
	if sp := m.spend(); sp != "" {
		l += " " + sp
	}
	if c.Status != core.Done {
		l += " " + memberDoing(m.board.At, c)
	}
	if c.Status != core.Busy && i != m.cursor {
		l = dim.Render(l)
	}
	return l + "\n"
}

func (m Model) itemLines(i int, it core.Item) string {
	line := m.mark(i) + pad(it.ID, 5) + " " + pad(it.State, 10) + " " + it.Question
	if it.State != core.ItemOpen && i != m.cursor {
		line = dim.Render(line)
	}
	out := line + "\n"
	if it.Answer != "" {
		out += dim.Render("         answer: "+it.Answer+delivered(it)) + "\n"
	}
	if it.Reason != "" {
		out += dim.Render("         dismissed: "+it.Reason+delivered(it)) + "\n"
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
func (m Model) field(b *strings.Builder, label, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	width := max(20, m.width-14)
	lines := strings.Split(ansi.Wrap(value, width, " "), "\n")
	for i, l := range lines {
		if i == 0 {
			b.WriteString(" " + bold.Render(pad(label, 11)) + " " + l + "\n")
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

func (m Model) sessionView() string {
	s, ok := m.session()
	if !ok {
		return dim.Render("   this session is gone; esc back")
	}
	var b strings.Builder
	title := s.Title
	if title == "" {
		title = core.Short(s.Key())
	}
	b.WriteString(bold.Render(" "+title) + dim.Render("  "+s.Harness+" "+s.Key()) + "\n\n")
	status := s.Status.String()
	if !s.Since.IsZero() {
		status += " since " + age(m.board.At, s.Since)
	}
	if s.WaitingFor != "" {
		status += " (" + s.WaitingFor + ")"
	}
	m.field(&b, "status", status)
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
		b.WriteString("\n" + bold.Render(" PULL REQUESTS") + "\n")
		for _, pr := range prsOf([]core.Live{*s}) {
			b.WriteString("   " + pad(fmt.Sprintf("%s#%d", pr.Repo, pr.Number), 34) + " " + prStatus(pr) + "\n")
		}
	}
	b.WriteString("\n" + bold.Render(fmt.Sprintf(" CREW (%d)", len(s.Crew))) + "\n")
	i := 0
	for _, c := range s.Crew {
		b.WriteString(m.crewLine(i, c))
		i++
	}
	if len(s.Crew) == 0 {
		b.WriteString(dim.Render("   none") + "\n")
	}
	if p, ok := m.board.Project(m.detail); ok {
		var items []core.Item
		for _, it := range p.Items {
			if it.Session == s.Key() {
				items = append(items, it)
			}
		}
		if len(items) > 0 {
			b.WriteString("\n" + bold.Render(" ITEMS") + "\n")
			for _, it := range items {
				b.WriteString(m.itemLines(i, it))
				i++
			}
		}
	}
	return b.String()
}

func prStatus(pr core.PR) string {
	if pr.Status == nil {
		return dim.Render("status not fetched yet")
	}
	st := pr.Status.String()
	if pr.Status.CI() == "failing" && !pr.Status.Closed() {
		st = alarm.Render(st)
	}
	if pr.Status.Title != "" {
		st += dim.Render("  " + pr.Status.Title)
	}
	return st
}

func (m Model) agentView() string {
	s, c, ok := m.member()
	if !ok {
		return dim.Render("   this agent is gone; esc back")
	}
	var b strings.Builder
	b.WriteString(bold.Render(" "+c.Name) + dim.Render("  in "+s.Title+"  "+c.ID) + "\n\n")
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
	m.field(&b, "model", c.Model)
	m.field(&b, "spend", m.spend())
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
		b.WriteString("\n" + bold.Render(" "+name) + "\n")
		for _, l := range strings.Split(ansi.Wrap(strings.TrimSpace(text), max(20, m.width-4), " "), "\n") {
			b.WriteString("   " + l + "\n")
		}
	}
	section("BRIEF", c.Brief)
	section("RESULT", c.Result)
	if len(c.Trail) > 0 {
		b.WriteString("\n" + bold.Render(fmt.Sprintf(" TRAIL (last %d actions)", len(c.Trail))) + "\n")
		for _, a := range c.Trail {
			at := ""
			if !a.At.IsZero() {
				at = a.At.Local().Format("15:04:05")
			}
			b.WriteString(dim.Render("   "+pad(at, 9)) + a.String() + "\n")
		}
	}
	return b.String()
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
 REPOS AND MISSIONS groups missions by repository; every worktree of a repo counts as the repo.
 A mission is a session plus the sessions it spawned. enter drills down:
 repo -> missions -> sessions and crew -> session detail -> agent brief, result and trail. esc goes up.

   ↑ ↓ or k j   move
   enter        an item: expand it; a permission prompt: go to its session; anything else: open it
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
