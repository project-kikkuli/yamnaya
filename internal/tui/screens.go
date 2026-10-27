package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/usage"
)

func (m Model) itemsView() viewPage {
	b := viewPage{}
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
	for _, p := range m.board.Profiles {
		account := p.Account
		if account == "" {
			account = "identity unknown"
		}
		b.WriteString(fmt.Sprintf("profile %s/%s [%s] %s\n", p.Harness, p.ID, p.Label, account+"  "+p.State))
	}
	if !f.Ready {
		b.WriteString("reading transcripts; spend fills in once the first scan ends\n")
	}
	usage.Render(&b, f, m.host, 15)
	for _, r := range m.board.Remotes {
		if r.Usage == nil {
			b.WriteString("usage on " + r.Host + ": unknown; snapshot has no profile usage\n")
		} else if r.Stale || r.Err != nil || r.Late(m.board.At) {
			b.WriteString("usage on " + r.Host + ": last observed " + r.Usage.At.Format(time.RFC3339) + " (" + r.State() + ")\n")
		}
	}
	return b.String()
}
