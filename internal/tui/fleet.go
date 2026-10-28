package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// staleAfter is how old a limit report may be before the header shows its age.
const staleAfter = 10 * time.Minute

// fleetLine shows each harness's account-wide limit windows and this host's API-priced spend of today and the last seven days
// in width columns, naming other hosts by tag; when it does not fit it drops report ages, then other hosts' reset times, then
// every reset time, then the spend, then other hosts' accounts from the end.
func fleetLine(f core.Fleet, now time.Time, width int, tag func(string) string) string {
	if !f.Ready && len(f.Limits) == 0 {
		return pal.dim.Render(" usage: reading transcripts…")
	}
	type window struct{ text, reset string }
	type account struct {
		label   string
		remote  bool
		windows []window
		seen    time.Time
	}
	var accounts []*account
	byLabel := map[string]*account{}
	for _, l := range f.Limits {
		a := byLabel[l.IdentityLabel()]
		if a == nil {
			local := l
			local.Host = ""
			label := local.IdentityLabel()
			if l.Host != "" {
				label = tag(l.Host) + "/" + label
			}
			a = &account{label: label, remote: l.Host != ""}
			byLabel[l.IdentityLabel()] = a
			accounts = append(accounts, a)
		}
		a.windows = append(a.windows, window{fmt.Sprintf("%s %.0f%%", l.Label(), l.Used), " ↻" + core.Span(l.ResetsAt.Sub(now))})
		if l.Used >= 80 {
			a.windows[len(a.windows)-1].text = pal.red.Render(a.windows[len(a.windows)-1].text)
		}
		if l.Seen.After(a.seen) {
			a.seen = l.Seen
		}
	}
	spend := pal.dim.Render("spend and codex limits: reading transcripts…")
	if f.Ready {
		spend = "spend today " + f.Today.Dollars() + " · 7d " + f.Week.Dollars()
	}
	render := func(ages, remoteResets, resets bool, shown []*account, spend string) string {
		var parts []string
		for _, a := range shown {
			var ws []string
			for _, w := range a.windows {
				if resets && (remoteResets || !a.remote) {
					ws = append(ws, w.text+w.reset)
				} else {
					ws = append(ws, w.text)
				}
			}
			p := pal.bold.Render(a.label) + " " + strings.Join(ws, " · ")
			if ages && now.Sub(a.seen) > staleAfter {
				p += pal.dim.Render(" (" + core.Span(now.Sub(a.seen)) + " old)")
			}
			parts = append(parts, p)
		}
		if spend != "" {
			parts = append(parts, spend)
		}
		return " " + strings.Join(parts, "   ")
	}
	fits := func(line string) bool { return width <= 0 || ansi.StringWidth(line) < width }
	for _, step := range [][3]bool{{true, true, true}, {false, true, true}, {false, false, true}, {false, false, false}} {
		if line := render(step[0], step[1], step[2], accounts, spend); fits(line) {
			return line
		}
	}
	shown := slices.Clone(accounts)
	if line := render(false, false, false, shown, ""); fits(line) {
		return line
	}
	for i := len(shown) - 1; i >= 0; i-- {
		if shown[i].remote {
			shown = slices.Delete(shown, i, i+1)
			if line := render(false, false, false, shown, ""); fits(line) {
				return line
			}
		}
	}
	return ansi.Truncate(render(false, false, false, shown, ""), max(20, width-1), "…")
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
