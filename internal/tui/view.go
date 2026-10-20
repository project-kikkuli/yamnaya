package tui

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/progress"
)

func (m Model) View() string {
	page := m.page
	if page == nil {
		prepared := m.prepareView()
		page = &prepared
	}
	return m.paintView(*page)
}

func (m Model) prepareView() viewPage {
	right := m.sceneWidth()
	if right > 0 {
		right += sceneGap
	}
	bodyModel := m
	bodyModel.width = max(20, m.width-right)
	var body viewPage
	switch m.mode {
	case modeHelp:
		body = textPage(ansi.Wrap(Help, bodyModel.width, " "), "")
	case modeScreen:
		body = textPage(m.screen, "")
	case modeWhy:
		body = textPage(ansi.Wrap(bodyModel.whyView(), bodyModel.width, " "), "")
	case modePick:
		body = textPage(bodyModel.pickView(), "")
	case modeInput:
		if m.inKind == inNewRepo {
			body = textPage(bodyModel.repoPickView(), "")
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
		case modeWork:
			body = bodyModel.workView()
		case modeReviews:
			body = bodyModel.reviewsView()
		case modeReview:
			body = bodyModel.reviewView()
		default:
			body = bodyModel.listView()
		}
	}
	sky := m.skylineHeight()
	var lines []viewLine
	for y := range sky {
		lines = append(lines, viewLine{scenery: 1, world: y})
	}
	if sky > 0 {
		lines = append(lines, viewLine{})
	}
	lines = append(lines, textPage(m.header()+"\n", "").lines...)
	top := len(lines)
	for len(body.lines) > 0 && body.lines[len(body.lines)-1].text == "" {
		body.lines = body.lines[:len(body.lines)-1]
	}
	for _, line := range body.lines {
		lines = append(lines, line)
	}
	var foot []viewLine
	for _, line := range m.footer() {
		foot = append(foot, textPage(ansi.Wrap(line, max(20, m.width-1), " "), "").lines...)
	}
	room := max(1, m.height-len(foot))
	if (m.mode == modeScreen || m.mode == modeAgent || m.mode == modeReview || (m.mode == modeWork && m.workPanel != 1)) && len(lines) > room && room > top {
		start := min(m.scrollY, len(lines)-room)
		lines = append(lines[:top:top], lines[top+start:min(len(lines), start+room)]...)
	} else if len(lines) > room {
		lines = m.scroll(lines, room, top)
	}
	layout := m.sceneLayout(top, room, len(lines))
	for len(lines) < room {
		lines = append(lines, viewLine{})
	}
	for y := range layout.lakeHeight {
		lines[layout.lakeStart+y].scenery = 2
		lines[layout.lakeStart+y].world = y
	}
	lines = append(lines, foot...)
	for i := range lines {
		width := m.width
		if i >= top && i < len(lines)-len(foot) {
			width -= right
		}
		lines[i].text = ansi.Truncate(lines[i].text, max(1, width), "…")
	}
	return viewPage{lines: lines, layout: layout}
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
	parts := []string{fmt.Sprintf("%d sessions", len(m.board.Sessions))}
	for _, st := range []core.Status{core.Busy, core.Waiting, core.Idle, core.Unknown} {
		if counts[st] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[st], st))
		}
	}
	h := pal.bold.Render(" ym") + "    " + strings.Join(parts, "  ")
	if len(m.board.WorkErrors) > 0 {
		h += "\n" + pal.yellow.Render(" coverage: "+strings.Join(m.board.WorkErrors, "; "))
	}
	if m.err != nil {
		h += "  " + pal.red.Render("refresh failed: "+m.err.Error())
	}
	if m.core != nil && m.core.Meter != nil {
		h += "\n" + fleetLine(m.board.Fleet, m.host, m.board.At)
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
	if m.mode == modeInput {
		repo := filepath.Base(m.newL.Repo)
		label := map[inputKind]string{inAnswer: "answer " + m.inLabel, inDismiss: "dismiss " + m.inLabel + " because", inRename: "rename to", inFilter: "filter",
			inWorkFeedback: "feedback; next action returns to author", inWorkDecision: "record judgment", inWorkMessage: "message " + m.inLabel, inCommand: "ym command",
			inDismissStale: "dismiss every stale item because", inSend: "send " + m.inLabel, inWaitAnswer: "answer " + m.inLabel,
			inNew: "new " + m.newL.Harness + " session in " + repo + ", prompt", inNewRepo: "new " + m.newL.Harness + " session, repository",
			inPhases: "phases of " + m.inLabel + " (a, b, c)", inVibe: "vibe of " + m.inLabel + " (any fitting place: why)"}[m.inKind]
		if m.inKind == inNew || m.inKind == inNewRepo {
			label += pal.dim.Render(" [tab: " + core.Other(m.newL.Harness) + "]")
		}
		if m.fresh {
			lines = append(lines, " "+pal.bold.Render(label+":")+" "+pal.dim.Render(string(m.input))+"█")
			lines = append(lines, pal.dim.Render(" enter accepts the default   type to replace it   esc cancel"))
			return lines
		}
		lines = append(lines, " "+pal.bold.Render(label+":")+" "+string(m.input)+"█")
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
	if m.width < 80 && (m.mode == modeList || m.mode == modeRepo || m.mode == modeDetail || m.mode == modeSession) {
		return append(lines, pal.dim.Render(" ↑↓ move   enter open   ? help"))
	}
	line := " "
	for _, hint := range strings.Split(m.hints(), "   ") {
		if ansi.StringWidth(line+hint) > max(20, m.width-1) && strings.TrimSpace(line) != "" {
			lines = append(lines, pal.dim.Render(strings.TrimRight(line, " ")))
			line = " "
		}
		line += hint + "   "
	}
	lines = append(lines, pal.dim.Render(strings.TrimRight(line, " ")))
	return lines
}

func (m Model) hints() string {
	switch m.mode {
	case modeWork:
		if m.workPanel == 1 {
			return "↑↓ choose workstream   enter open   s message   B brief   C context   tab next panel   esc back"
		}
		if m.workPanel == 2 {
			return "↑↓ scroll   e edit context   B brief   tab next panel   s message manager   esc back"
		}
		return "↑↓ scroll   space/b page   tab brief/work/context/history   C context   s message manager   enter open workstream   R reviews   V rich brief   : command   esc back"
	case modeReviews:
		return "↑↓ move   enter review   tab ready/preparing/author/completed   B brief   : command   esc back"
	case modeReview:
		return "↑↓ scroll   a judgment   f feedback   B manager brief   V source   esc back"
	case modeHelp, modeScreen:
		return "esc back"
	case modeItems:
		return "↑↓ move   enter expand   a answer   g go ahead   l later   u unsnooze   x dismiss   esc back"
	case modeWhy:
		return "a answer   g go ahead   l later   x dismiss   u unsnooze   o go to its session   O beside   esc back"
	case modePick:
		return "↑↓ choose   enter open   esc back"
	case modeMerge:
		return "↑↓ choose mission   enter merge into it   esc cancel"
	case modeRepo:
		if m.workAction != "" {
			return "↑↓ choose project   enter open   B brief   C context   esc back"
		}
		return "↑↓ move   enter open mission   o go to session   O beside   n new   N new in any repo   K kill   p/P phase   v vibe   r rename   m merge   S split   I items   esc back   ? help"
	case modeDetail:
		return "↑↓ move   enter open session, agent or item   o go to session   O beside   s send   K kill   L land   F forget   T switch   n new   N new in any repo   a answer   g go ahead   l later   u unsnooze   x dismiss   I all items   p/P phase   v vibe   r rename   esc back   ? help"
	case modeSession:
		return "↑↓ move   enter open agent or item   o go to session   O beside   s send   K kill   L land   F forget   T switch   n new   N new in any repo   a answer   g go ahead   x dismiss   I all items   p/P phase   v vibe   esc back   ? help"
	case modeAgent:
		return "↑↓ scroll   space/b page   o go to its session   O beside   s send   K kill   L land   F forget   T switch   esc back"
	}
	return "↑↓ move   enter open   B brief   R reviews   C context   V artifact   s message   a answer   g go ahead   : command   o session   n new   / filter   I items   U usage   A scenery   ? help   q quit"
}

func (m Model) mark(i int) string {
	if i == m.cursor {
		return " > "
	}
	return "   "
}

func (m Model) listView() viewPage {
	if !m.loaded {
		return viewPage{}
	}
	b := viewPage{place: ""}
	rows := m.rows()
	needs := 0
	for _, r := range rows {
		if r.review != nil || (r.need != nil && !m.isStale(r.need)) {
			needs++
		}
	}
	b.WriteString(pal.bold.Render(fmt.Sprintf(" NEEDS YOU (%d)", needs)) + "\n")
	if needs == 0 {
		b.WriteString(pal.dim.Render("   nothing") + "\n")
	}
	repos := false
	nativeProgress := false
	for _, r := range rows {
		if r.project != nil && r.project.Work == nil {
			nativeProgress = true
			break
		}
	}
	cols := m.missionCols(2)
	for i, r := range rows {
		if r.review == nil && (r.repo != nil || r.project != nil) && !repos {
			repos = true
			b.WriteString("\n" + pal.bold.Render(" REPOS AND MISSIONS") + "\n")
			if cols.bar > 0 && nativeProgress {
				b.WriteString(pal.dim.Render("   "+progressLegend) + "\n")
			}
		}
		switch {
		case r.review != nil:
			b.WriteString(m.mark(i) + pal.yellow.Render(pad(r.review.Kind, 9)) + " " + pad(r.work.Name, min(28, max(12, m.width/4))) + " " + r.review.Title + "\n")
			if i == m.cursor {
				m.field(&b, "ask", r.review.Question)
				m.field(&b, "rec", r.review.Recommendation)
				m.field(&b, "changed", r.review.Changed)
			}
		case r.stale:
			line := m.mark(i) + pal.dim.Render(fmt.Sprintf("stale (%d)", len(m.board.Stale))) + pal.dim.Render(" open items whose sessions ended")
			if i == m.cursor {
				line += pal.dim.Render("   enter expand   x dismiss all")
			}
			b.WriteString(line + "\n")
		case r.need != nil:
			b.place = "m:" + r.need.Project
			b.WriteString(m.needLines(i, *r.need))
		case r.project == nil:
			b.place = "r:" + r.repo.Key
			b.WriteString(m.repoLine(i, *r.repo, cols))
		default:
			b.place = "m:" + r.project.Key
			b.WriteString(m.missionLine(i, *r.project, cols))
		}
	}
	if !repos {
		b.WriteString("\n" + pal.bold.Render(" REPOS AND MISSIONS") + "\n" + pal.dim.Render("   no sessions found") + "\n")
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
	seen := map[string]bool{}
	for _, p := range r.Missions {
		if p.Work != nil && !seen[p.Work.ID] {
			n += p.Work.Ready()
			seen[p.Work.ID] = true
		}
	}
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

type prTally struct{ open, fail, run, pass, merged int }

// tallyPRs counts open pull requests by CI result, then merged ones; closed ones count nowhere.
func tallyPRs(prs []core.PR) prTally {
	var t prTally
	for _, pr := range prs {
		switch {
		case pr.Status != nil && pr.Status.State == "MERGED":
			t.merged++
		case pr.Status != nil && pr.Status.State == "CLOSED":
		default:
			t.open++
			if pr.Status != nil {
				switch pr.Status.CI() {
				case "failing":
					t.fail++
				case "running":
					t.run++
				case "passing":
					t.pass++
				}
			}
		}
	}
	return t
}

// prSummary counts open pull requests by CI result, then merged ones.
func prSummary(prs []core.PR) string {
	t := tallyPRs(prs)
	open, fail, run, pass, merged := t.open, t.fail, t.run, t.pass, t.merged
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
		in[s.Key()] = true
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
	if sp, ok := f.Agents[s.Key()+"/"+c.ID]; ok {
		return spendText(f, sp)
	}
	if ss, ok := f.Sessions[c.ID]; ok {
		return spendText(f, ss.Total)
	}
	return core.Spend{}.Dollars()
}

func (m Model) needLines(i int, n core.Need) string {
	name := pad(m.projectName(n.Project), min(32, max(16, m.width/4)))
	sel := i == m.cursor
	var b strings.Builder
	if n.Waiting() {
		s := n.Session
		head := m.mark(i) + m.hue(n.Project).Render(pad("wait", m.idWidth())) + " " + name + " " + pad(titleOf(*s), 20) + " waiting: "
		b.WriteString(head + pad(n.Line, max(10, m.width-ansi.StringWidth(head)-1)) + "\n")
		if sel {
			b.WriteString(pal.dim.Render("         "+s.Location()+" | "+age(m.board.At, s.Since)) + "\n")
			b.WriteString(pal.dim.Render("         enter last reply   a answer   x dismiss   o go to session   O beside") + "\n")
		}
		return b.String()
	}
	if n.Item == nil {
		what := "permission prompt"
		if n.Session.Prompt != nil {
			what = n.Session.Prompt.String()
		}
		b.WriteString(m.mark(i) + pal.yellow.Render("PERM ") + " " + name + " " + what + "\n")
		if sel {
			b.WriteString(pal.dim.Render("         enter "+goVerb(*n.Session)+"   s send") + "\n")
		}
		return b.String()
	}
	it := n.Item
	q, keys := it.Question, "enter expand   a answer   l later   x dismiss   o go to session   O beside"
	if it.Kind == core.KindGo {
		q, keys = pal.yellow.Render("go ahead? ")+q, "enter expand   g go ahead   a answer   l later   x dismiss   o go to session   O beside"
	}
	b.WriteString(m.mark(i) + m.hue(n.Project).Render(pad(m.itemID(*it), m.idWidth())) + " " + name + " " + q + "\n")
	var meta []string
	if it.Rec != "" {
		meta = append(meta, "rec: "+it.Rec)
	}
	meta = append(meta, fmt.Sprintf("blocks %d", it.Blocks), "src: "+m.sourceName(*it), age(m.board.At, it.CreatedAt))
	if sel {
		b.WriteString(pal.dim.Render("         "+strings.Join(meta, " | ")) + "\n")
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
	heading := "MISSIONS"
	if m.workAction == "B" {
		heading = "CHOOSE A PROJECT BRIEF"
	}
	if m.workAction == "C" {
		heading = "CHOOSE PROJECT CONTEXT"
	}
	b.WriteString("\n" + pal.bold.Render(" "+heading) + "\n")
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
	if pr := prSummary(prsOf(p.Sessions)); pr != "" {
		b.WriteString(" prs     " + pr + "\n")
	}
	pg := progress.Of(p)
	if f, ok := pg.Fraction(); ok {
		b.WriteString(" progress " + progress.Bar(f, 20) + fmt.Sprintf(" %3.0f%%  ", f*100) + pal.dim.Render(pg.Parts()) + "\n")
	}
	b.WriteString(units(pg.Units))
	b.WriteString(m.vibeLines(p))
	b.WriteString("\n" + pal.bold.Render(" SESSIONS AND CREW") + "\n")
	i := 0
	for _, s := range p.Sessions {
		title := s.Title
		if title == "" {
			title = core.Short(s.Key())
		}
		line := m.mark(i) + pad(title, m.nameWidth()) + " " + pad(s.Harness, 7) + pal.state(s.Status.String()).Render(pad(s.Status.String(), 9)) + pad(s.Location(), 18)
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
		b.WriteString(line + "\n")
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
		line := m.mark(i) + pad(title, m.nameWidth()) + " " + pad(d.Harness, 7) + pal.dim.Render(pad(d.State, 9)) + pad(d.Key, 18) + " " + d.Branch
		if i == m.cursor {
			line += pal.dim.Render("   enter revive")
			if ansi.StringWidth(d.State) > 9 {
				line += "   " + pal.red.Render(d.State)
			}
		}
		b.WriteString(line + "\n")
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
	l := m.mark(i) + "  └ " + pad(c.Name, max(16, m.nameWidth()-4)) + " " + pad(kind, 16) + " " + pad(c.Status.String(), 8) + " " + pad(shortModel(c.Model), 10)
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
	line := m.mark(i) + ids[it.Key()] + " " + pad(it.State, 10) + " " + it.Question
	if it.State != core.ItemOpen && i != m.cursor {
		line = pal.dim.Render(line)
	}
	out := line + "\n"
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
	b.WriteString(pal.bold.Render(" "+title) + pal.dim.Render("  "+s.Harness+" "+s.Key()) + "\n\n")
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
			state := map[string]string{core.MsgPending: "pending", core.MsgTyped: "typed", core.MsgInbox: "via inbox", core.MsgSent: "sent"}[msg.State]
			when := age(m.board.At, msg.CreatedAt)
			if !msg.Delivered.IsZero() {
				when = "delivered " + age(m.board.At, msg.Delivered)
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
	field("blocks", fmt.Sprint(it.Blocks))
	field("source", m.sourceName(*it)+"  "+it.Session)
	field("filed", it.CreatedAt.Local().Format("2006-01-02 15:04"))
	field("answer", it.Answer)
	field("dismissed", it.Reason)
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

const Help = ` ym shows every Claude Code and Codex session and what needs you.

   B            read the full manager brief; on a repo choose its project first; V opens the browser version
   R            all review obligations; tab switches ready, preparing, with author, and completed
   C            read project context; e opens $EDITOR from the context panel, preserving its starting revision
   V            open the review's original source or the project's stable rich brief in the browser
   :            run an owner command without leaving the board, for example work watch ID URL --kind notion

 NEEDS YOU lists permission prompts first, then open items, most blocking first, then idle sessions
 whose last reply asks you with no item filed ("waiting:", the first question it asks).
 REPOS AND MISSIONS groups missions by repository; every worktree of a repo counts as the repo.
 A mission is a session plus the sessions it spawned. enter drills down:
 repo -> missions -> sessions and crew -> session detail -> agent brief, result and trail. esc goes up.

   ↑ ↓ or k j   move
   A            hide or show scenery
   enter        an item: expand it; a permission prompt: go to its session; a waiting session: show its
                last reply; an exited launch: revive it; anything else: open it
   o            go to the selected row's session: jump to its tmux pane, or say where it runs;
                a session in another terminal app (iTerm, Terminal) is adopted: once idle, ym ends
                it there and resumes it on its own server
   O            open the selected row's session in a new tmux pane beside ym, so the board stays in
                view; needs ym running inside tmux
   s            send the session a follow-up: typed in on ym's server even mid-turn, held while a
                permission prompt shows; other sessions read it from ym item inbox
   a            answer an item; an idle agent in tmux gets it typed in, others on their next turn;
                on a waiting session, send the answer as your message, the same way s does, and clear the row
   g            go ahead: answer a go item with "PLEASE GO IMPLEMENT THIS"
   l            later: snooze the item
   u            reopen a snoozed item (detail)
   x            dismiss the item with a reason; on a waiting session, hide the row until it asks again
   w            show the item in full with its history, or a waiting session's last reply
   r            rename a mission; an empty name restores the inferred one
   m            merge a mission into another
   S            split a merged mission back out
   n            new session in the selected row's repository (else ym's own directory): type the
                prompt; the worktree is named from it; tab switches claude and codex
   N            new session in any repository: pick it first, then the prompt; the picker lists
                recently picked repos, then every repo under ~/.config/wt-reap/roots (or ~/dev); typing
                filters by name or path, ↑↓ and enter pick, and a path starting with / or ~ is used as typed
   K            kill the session's claude or codex process with SIGTERM (asks y/n); its terminal,
                worktree and transcript stay, and a session ym launched shows exited, ready to revive
   L            land a session ym launched: end it and remove its worktree once its branch is clean
                and merged or pushed, deleting the branch when merged into origin (asks y/n); on an exited
                mission, every launch in it. One whose agent exited or whose pane is gone, or one killed
                by a signal over a day ago, lands by itself when it has nothing to lose
   F            forget a session ym launched: end it and drop its launch record, leaving its worktree and
                branch as they are (asks y/n); on an exited mission, every launch in it
   G            gc: remove worktrees of known repos idle 3 days that are clean, on a remote and unused,
                then local branches merged into origin that no worktree uses (asks y/n); the board
                runs it by itself at most hourly
   T            end the idle session and relaunch it in the other harness on ym's server (asks y/n)
   p            advance the mission to its next phase (asks y/n)
   P            set the mission's phases, comma separated
   v            set the mission's vibe, any fitting place from Afghanistan (broken) to Hawaii (great): "place: why"
   I            every item, answered, dismissed and snoozed too; in a mission, just its items
   /            filter
   h            health: weekly item answer times, stale items, questions reprinted in chat, time blocked on you
   U            usage: spend today and this week, limits, and the top sessions by spend
   E            every session ym launched and whether its agent still runs
   q            back or quit

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
