package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/signals"
)

var shortLabels = map[signals.State]string{
	signals.NeedsPermission: "perm",
	signals.Asking:          "asks",
	signals.Running:         "run",
	signals.Dead:            "dead",
}

func shortLabel(w signals.Window, now time.Time) string {
	st := w.State(now)
	if st == signals.Idle {
		return "idle " + signals.Duration(now.Sub(w.LastActive))
	}
	return shortLabels[st]
}

func (m *boardModel) sidebarWidth() int {
	if m.width > 0 {
		return m.width
	}
	return sidebarWidth
}

func (m *boardModel) sidebarView() []string {
	now := m.now()
	lines := []string{styleDim.Render(" ym"), ""}
	if len(m.snap.Projects) == 0 {
		lines = append(lines, styleDim.Render("  no projects · n new"))
	}
	for _, p := range m.snap.Projects {
		selected := m.sel.kind == rowProject && m.sel.project == p.Name
		var flags []string
		if n := p.NeedPermission(); n > 0 {
			flags = append(flags, styleNeed.Render(fmt.Sprintf("!%d", n)))
		}
		if p.Open > 0 {
			flags = append(flags, styleAsk.Render(fmt.Sprintf("?%d", p.Open)))
		}
		if d := p.Dead(); d > 0 {
			flags = append(flags, styleBad.Render(fmt.Sprintf("x%d", d)))
		}
		style := styleDim
		if selected || p.Open > 0 || p.NeedPermission() > 0 {
			style = stylePlain
		}
		head := style.Render(fmt.Sprintf("%s%-13s %-5s", marker(selected), ansi.Truncate(p.Name, 13, "…"), ansi.Truncate(phaseDots(p.Project), 5, "")))
		lines = append(lines, strings.TrimRight(head+" "+strings.Join(flags, " "), " "))
		if !m.expandedProject(p) {
			continue
		}
		for _, w := range p.Windows {
			sel := m.sel.kind == rowWindow && m.sel.project == p.Name && m.sel.window == w.Name
			style := styleDim
			switch w.State(now) {
			case signals.NeedsPermission:
				style = styleNeed
			case signals.Asking:
				style = styleAsk
			case signals.Dead:
				style = styleBad
			default:
				if sel || w.Staged {
					style = stylePlain
				}
			}
			stage := " "
			if w.Staged {
				stage = "»"
			}
			lines = append(lines, style.Render(fmt.Sprintf(" %s%s%-14s %s", stage, marker(sel), ansi.Truncate(w.Name, 14, "…"), shortLabel(w, now))))
			if w.State(now) == signals.NeedsPermission {
				lines = append(lines, styleNeed.Render("     "+w.Permission.String()))
			}
		}
	}
	if len(m.snap.Sessions) > 0 {
		lines = append(lines, "", styleDim.Render("  sessions"))
		for _, ses := range m.snap.Sessions {
			sel := m.sel.kind == rowSession && m.sel.target == ses.Pane
			style := styleDim
			if sel || ses.Loud() {
				style = stylePlain
			}
			lines = append(lines, style.Render(fmt.Sprintf("  %s%-16s %s", marker(sel), ansi.Truncate(sessionTitle(ses), 16, "…"), ses.Label(now))))
			if !m.expandedSession(ses) {
				continue
			}
			shown, _ := shownCrew(ses.Crew)
			for _, c := range shown {
				csel := m.sel.kind == rowCrew && m.sel.target == ses.Pane && m.sel.window == c.ID
				style := styleDim
				if csel {
					style = stylePlain
				}
				lines = append(lines, style.Render(fmt.Sprintf("    %s%-16s %s", marker(csel), ansi.Truncate(c.Label(), 16, "…"), c.Status)))
			}
		}
	}
	return append(lines, "",
		styleDim.Render("  ⏎ stage  ⇥ focus  o items"),
		styleDim.Render("  n new  k kurgan  t thread"))
}

func (m *boardModel) sidebarItemsView() []string {
	now := m.now()
	width := m.sidebarWidth()
	lines := []string{" " + m.itemsOf + styleDim.Render(" · items"), ""}
	if len(m.items) == 0 {
		lines = append(lines, styleDim.Render("  nothing open"))
	}
	for i, it := range m.items {
		sel := i == m.itemSel
		style := styleDim
		if sel {
			style = stylePlain
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%-4s %s", marker(sel), it.ID, it.Question)))
		if m.expanded != it.ID {
			continue
		}
		source := it.SourceWindow
		if source == "" {
			source = "manager"
		}
		var detail []string
		detail = append(detail, it.Question)
		if it.Rec != "" {
			detail = append(detail, "rec "+it.Rec)
		}
		for _, o := range it.Options {
			detail = append(detail, o.Label+" → "+o.Effect)
		}
		if it.Assumption != "" {
			detail = append(detail, "assumes "+it.Assumption)
		}
		detail = append(detail, "from "+source+" · "+signals.Duration(now.Sub(it.CreatedAt)))
		for _, d := range detail {
			for _, l := range strings.Split(ansi.Wordwrap(d, width-4, " "), "\n") {
				lines = append(lines, "    "+l)
			}
		}
	}
	return append(lines, "",
		styleDim.Render("  ⏎ open  g go  f follow up"),
		styleDim.Render("  y/n  d dismiss  s snooze"))
}

func (m *boardModel) kurganView() []string {
	now := m.now()
	lines := []string{" kurgan", ""}
	if len(m.kurgan) == 0 {
		lines = append(lines, styleDim.Render("  nothing landed"))
	}
	for i, p := range m.kurgan {
		sel := i == m.kurganSel
		style := styleDim
		if sel {
			style = stylePlain
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%-16s %s", marker(sel), ansi.Truncate(p.Name, 16, "…"), signals.Duration(now.Sub(p.LandedAt)))))
	}
	return append(lines, "", styleDim.Render("  ⏎ revive  esc back"))
}
