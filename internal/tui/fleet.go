package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// staleAfter is how old a limit report may be before the header shows its age.
const staleAfter = 10 * time.Minute

// fleetLine shows each harness's account-wide limit windows and this host's API-priced spend of today and the last seven days.
func fleetLine(f core.Fleet, host string, now time.Time) string {
	if !f.Ready && len(f.Limits) == 0 {
		return pal.dim.Render(" usage: reading transcripts…")
	}
	var parts []string
	for _, h := range []string{"claude", "codex"} {
		var ws []string
		var seen time.Time
		for _, l := range f.Limits {
			if l.Harness != h {
				continue
			}
			w := fmt.Sprintf("%s %.0f%% ↻%s", l.Label(), l.Used, core.Span(l.ResetsAt.Sub(now)))
			if l.Used >= 80 {
				w = pal.red.Render(w)
			}
			ws = append(ws, w)
			if l.Seen.After(seen) {
				seen = l.Seen
			}
		}
		if len(ws) == 0 {
			continue
		}
		p := pal.bold.Render(h) + " " + strings.Join(ws, " · ")
		if now.Sub(seen) > staleAfter {
			p += pal.dim.Render(" (" + core.Span(now.Sub(seen)) + " old)")
		}
		parts = append(parts, p)
	}
	if f.Ready {
		parts = append(parts, "api spend on "+host+" today "+f.Today.Dollars()+" · 7d "+f.Week.Dollars())
	} else {
		parts = append(parts, pal.dim.Render("spend and codex limits: reading transcripts…"))
	}
	return " " + strings.Join(parts, "   ")
}

// spendText is the spend at API prices and its approximate share of each current limit window.
func spendText(f core.Fleet, s core.Spend) string {
	if sh := f.Shares(s); sh != "" {
		return s.Dollars() + " " + pal.dim.Render(sh)
	}
	return s.Dollars()
}
