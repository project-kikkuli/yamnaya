package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// staleAfter is how old a limit report may be before the header shows its age.
const staleAfter = 10 * time.Minute

// fleetLine shows each harness's account-wide limit windows and this host's API-priced spend of today and the last seven days,
// naming other hosts by tag and wrapping between accounts to fit width columns.
func fleetLine(f core.Fleet, now time.Time, width int, tag func(string) string) string {
	if !f.Ready && len(f.Limits) == 0 {
		return pal.dim.Render(" usage: reading transcripts…")
	}
	var labels []string
	byLabel := map[string][]core.Limit{}
	for _, l := range f.Limits {
		if byLabel[l.IdentityLabel()] == nil {
			labels = append(labels, l.IdentityLabel())
		}
		byLabel[l.IdentityLabel()] = append(byLabel[l.IdentityLabel()], l)
	}
	var parts []string
	for _, id := range labels {
		ls := byLabel[id]
		local := ls[0]
		local.Host = ""
		label := local.IdentityLabel()
		if ls[0].Host != "" {
			label = tag(ls[0].Host) + "/" + label
		}
		var ws []string
		var seen time.Time
		for _, l := range ls {
			w := fmt.Sprintf("%s %.0f%% ↻%s", l.Label(), l.Used, core.Span(l.ResetsAt.Sub(now)))
			if l.Used >= 80 {
				w = pal.red.Render(w)
			}
			ws = append(ws, w)
			if l.Seen.After(seen) {
				seen = l.Seen
			}
		}
		p := pal.bold.Render(label) + " " + strings.Join(ws, " · ")
		if now.Sub(seen) > staleAfter {
			p += pal.dim.Render(" (" + core.Span(now.Sub(seen)) + " old)")
		}
		parts = append(parts, p)
	}
	if f.Ready {
		parts = append(parts, "spend today "+f.Today.Dollars()+" · 7d "+f.Week.Dollars())
	} else {
		parts = append(parts, pal.dim.Render("spend and codex limits: reading transcripts…"))
	}
	return wrapItems("usage", parts, width)
}

// wrapItems puts items after label on as many lines as width needs, breaking only between items and indenting each
// continuation under the first item; width 0 keeps one line.
func wrapItems(label string, items []string, width int) string {
	const sep = "   "
	lead := " " + label + " "
	indent := strings.Repeat(" ", ansi.StringWidth(lead))
	var lines []string
	line, used := " "+pal.dim.Render(label)+" ", ansi.StringWidth(lead)
	for i, it := range items {
		w := ansi.StringWidth(it)
		if i > 0 && width > 0 && used+len(sep)+w >= width {
			lines = append(lines, line)
			line, used = indent, len(indent)
		} else if i > 0 {
			line, used = line+sep, used+len(sep)
		}
		line, used = line+it, used+w
	}
	return strings.Join(append(lines, line), "\n")
}

// spendText is the spend at API prices and its approximate share of each current limit window.
func spendText(f core.Fleet, s core.Spend) string {
	if sh := f.Shares(s); sh != "" {
		return s.Dollars() + " " + pal.dim.Render(sh)
	}
	return s.Dollars()
}

func profileDisplay(h, id string) string {
	if id == "" || id == "default" {
		return h
	}
	return h + "/" + id
}
