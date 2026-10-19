package tui

import (
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

func (m Model) itemsView() viewPage {
	b := viewPage{place: "m:" + m.itemsOf}
	title := "every project"
	if p, ok := m.board.Project(m.itemsOf); ok && m.itemsOf != "" {
		title = p.Name
	}
	b.WriteString(pal.bold.Render(" ALL ITEMS") + pal.dim.Render("  "+title+", answered, dismissed and snoozed too") + "\n")
	if len(m.items) == 0 {
		b.WriteString(pal.dim.Render("   none") + "\n")
	}
	ids := itemIDs(m.items)
	for i, it := range m.items {
		b.place = m.itemPlace(it.Session)
		b.WriteString(m.itemLines(i, it, ids))
	}
	return b
}

func (m Model) usageText() string {
	if m.core.Meter == nil {
		return " usage needs the board's transcript meter, which is off"
	}
	var b strings.Builder
	f := m.board.Fleet
	if !f.Ready {
		b.WriteString("reading transcripts; spend fills in once the first scan ends\n")
	}
	usage.Render(&b, f, m.host, 15)
	return b.String()
}

func (m Model) launchedText() string {
	if m.core.Launches == nil {
		return " ym's launcher is unavailable"
	}
	var ls []core.Launched
	for _, l := range m.core.Launches.Launched() {
		if l.Landed.IsZero() {
			ls = append(ls, l)
		}
	}
	if len(ls) == 0 {
		return " ym launched no sessions yet; n starts one"
	}
	var b strings.Builder
	for _, l := range ls {
		b.WriteString(l.String() + "\n")
	}
	return b.String()
}
