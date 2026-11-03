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
	limits := f.PerHost
	if limits == nil {
		limits = f.Limits
	}
	var labels []string
	byLabel := map[string][]core.Limit{}
	for _, l := range limits {
		if byLabel[l.IdentityLabel()] == nil {
			labels = append(labels, l.IdentityLabel())
		}
		byLabel[l.IdentityLabel()] = append(byLabel[l.IdentityLabel()], l)
	}
	var parts [][]string
	for _, id := range labels {
		ls := byLabel[id]
		local := ls[0]
		local.Host = ""
		host := "local"
		if ls[0].Host != "" {
			host = tag(ls[0].Host)
		}
		row := []string{pal.bold.Render(host + " " + local.IdentityLabel())}
		var seen time.Time
		for i, l := range ls {
			w := fmt.Sprintf("%s %.0f%% ↻%s", l.Label(), l.Used, core.Span(l.ResetsAt.Sub(now)))
			if i > 0 {
				w = "· " + w
			}
			if l.Used >= 80 {
				w = pal.red.Render(w)
			}
			row = append(row, w)
			if l.Seen.After(seen) {
				seen = l.Seen
			}
		}
		if now.Sub(seen) > staleAfter {
			row = append(row, pal.dim.Render("("+core.Span(now.Sub(seen))+" old)"))
		}
		parts = append(parts, row)
	}
	if f.Ready {
		parts = append(parts, []string{"spend today " + f.Today.Dollars(), "· 7d " + f.Week.Dollars()})
	} else {
		parts = append(parts, []string{pal.dim.Render("spend and codex limits: reading transcripts…")})
	}
	return wrapRows("usage", parts, width)
}

// wrapItems puts items after label on as many lines as width needs, breaking only between items and indenting each
// continuation under the first item; width 0 keeps one line.
func wrapItems(label string, items []string, width int) string {
	rows := make([][]string, len(items))
	for i, it := range items {
		rows[i] = []string{it}
	}
	return wrapRows(label, rows, width)
}

// wrapRows is wrapItems for rows of unbreakable cells: a row that fits no line of its own breaks between its cells.
func wrapRows(label string, rows [][]string, width int) string {
	const sep = "   "
	lead := " " + label + " "
	indent := strings.Repeat(" ", ansi.StringWidth(lead))
	var lines []string
	line, used := " "+pal.dim.Render(label)+" ", ansi.StringWidth(lead)
	breakTo := func(prefix string) {
		lines = append(lines, line)
		line, used = prefix, len(prefix)
	}
	for i, row := range rows {
		w := ansi.StringWidth(strings.Join(row, " "))
		if i > 0 && width > 0 && used+len(sep)+w >= width {
			breakTo(indent)
		} else if i > 0 {
			line, used = line+sep, used+len(sep)
		}
		for j, cell := range row {
			cw := ansi.StringWidth(cell)
			if j > 0 && width > 0 && used+1+cw >= width {
				breakTo(indent + "  ")
			} else if j > 0 {
				line, used = line+" ", used+1
			}
			line, used = line+cell, used+cw
		}
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

// Goal31Line exposes fleetLine to the throwaway render program in goal31-mockups.
func Goal31Line(f core.Fleet, now time.Time, width int, tag func(string) string) string {
	return ansi.Strip(fleetLine(f, now, width, tag))
}

// Goal31Today renders HEAD's fleetLine and the board's paint truncation, for the same throwaway program.
func Goal31Today(f core.Fleet, now time.Time, width int, tag func(string) string) string {
	lines := strings.Split(ansi.Strip(fleetLineToday(f, now, width, tag)), "\n")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "…")
	}
	return strings.Join(lines, "\n")
}
