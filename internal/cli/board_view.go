package cli

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/signals"
	"github.com/project-kikkuli/yamnaya/internal/store"
)

var (
	styleDim   = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	styleNeed  = lipgloss.NewStyle().Foreground(lipgloss.Color("201")).Bold(true)
	styleAsk   = lipgloss.NewStyle().Foreground(lipgloss.Color("214")).Bold(true)
	styleBad   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	stylePlain = lipgloss.NewStyle()
)

func phaseDots(p store.Project) string {
	var b strings.Builder
	for i := range p.Phases {
		if i <= p.PhaseIndex {
			b.WriteString("●")
		} else {
			b.WriteString("○")
		}
	}
	return b.String()
}

func marker(selected bool) string {
	if selected {
		return "› "
	}
	return "  "
}

func (m *boardModel) View() string {
	var lines []string
	if m.itemsOf != "" {
		lines = m.itemsView()
	} else {
		lines = m.projectsView()
	}
	lines = append(lines, "")
	if m.prompt != nil {
		f := m.prompt.fields[m.prompt.at]
		lines = append(lines, f.label+": "+f.value+"█")
	} else if m.status != "" {
		lines = append(lines, styleBad.Render(m.status))
	}
	if m.width > 0 {
		for i, l := range lines {
			lines[i] = ansi.Truncate(l, m.width, "…")
		}
	}
	return strings.Join(lines, "\n")
}

func (m *boardModel) projectsView() []string {
	now := m.now()
	var lines []string
	if len(m.snap.Projects) == 0 {
		lines = append(lines, styleDim.Render("  no projects"))
	}
	for _, p := range m.snap.Projects {
		selected := m.sel.kind == rowProject && m.sel.project == p.Name
		var flags []string
		if n := p.NeedPermission(); n > 0 {
			flags = append(flags, styleNeed.Render(fmt.Sprintf("%d needs permission", n)))
		}
		if p.Open > 0 {
			flags = append(flags, styleAsk.Render(fmt.Sprintf("%d asks you", p.Open)))
		}
		if d := p.Dead(); d > 0 {
			flags = append(flags, styleBad.Render(fmt.Sprintf("%d dead", d)))
		}
		phase := ""
		if len(p.Phases) > 0 {
			phase = p.Phases[p.PhaseIndex]
		}
		style := styleDim
		if selected || p.Open > 0 || p.NeedPermission() > 0 {
			style = stylePlain
		}
		head := style.Render(fmt.Sprintf("%s%-20s %-8s %-12s", marker(selected), p.Name, phaseDots(p.Project), phase))
		lines = append(lines, strings.TrimRight(head+" "+strings.Join(flags, "  "), " "))
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
				if sel {
					style = stylePlain
				}
			}
			harnessLabel := w.Harness
			if !w.Managed {
				harnessLabel += "*"
			}
			lines = append(lines, style.Render(fmt.Sprintf("    %s%-18s %-7s %s", marker(sel), w.Name, harnessLabel, w.Label(now))))
		}
	}
	if len(m.snap.Unmanaged) > 0 {
		lines = append(lines, "", styleDim.Render("  unmanaged"))
		for _, u := range m.snap.Unmanaged {
			sel := m.sel.kind == rowUnmanaged && m.sel.target == u.ID
			style := styleDim
			if sel {
				style = stylePlain
			}
			lines = append(lines, style.Render(fmt.Sprintf("    %s%-26s %s", marker(sel), fmt.Sprintf("%s:%d %s", u.Session, u.Index, u.Name), u.Command)))
		}
	}
	lines = append(lines, "", styleDim.Render("  ↑↓ move  ⏎ jump  o items  t thread  a adopt  q quit"))
	return lines
}

func (m *boardModel) itemsView() []string {
	now := m.now()
	lines := []string{"  " + m.itemsOf + styleDim.Render(" · open items"), ""}
	if len(m.items) == 0 {
		lines = append(lines, styleDim.Render("  nothing open"))
	}
	for i, it := range m.items {
		sel := i == m.itemSel
		style := styleDim
		if sel {
			style = stylePlain
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%-4s %-8s %s", marker(sel), it.ID, it.Kind, it.Question)))
		if m.expanded != it.ID {
			continue
		}
		detail := func(label, value string) {
			if value != "" {
				lines = append(lines, "       "+styleDim.Render(label)+" "+value)
			}
		}
		detail("rec", it.Rec)
		for _, o := range it.Options {
			detail("option", o.Label+" → "+o.Effect)
		}
		detail("assumes", it.Assumption)
		source := it.SourceWindow
		if source == "" {
			source = store.RoleManager
		}
		detail("from", source)
		detail("age", signals.Duration(now.Sub(it.CreatedAt)))
	}
	lines = append(lines, "", styleDim.Render("  ⏎ expand  g go  f follow up  y/n answer default  d dismiss  s snooze  esc back"))
	return lines
}
