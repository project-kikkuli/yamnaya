package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// localHost labels this host's rows beside other hosts' @nicknames.
const localHost = "local"

// staleAfter is how old a limit report may be before the header shows its age.
const staleAfter = 10 * time.Minute

// fleetLine shows every host's limit windows for each login and the API-priced spend of today and the last seven days,
// naming each row's host and wrapping between rows, then between a row's cells, to fit width columns.
func fleetLine(f core.Fleet, now time.Time, width int, tag func(string) string) string {
	if !f.Ready && len(f.PerHost) == 0 {
		return pal.dim.Render(" usage: reading transcripts…")
	}
	var labels []string
	byLabel := map[string][]core.Limit{}
	for _, l := range shownOrder(f.PerHost, tag) {
		if byLabel[l.IdentityLabel()] == nil {
			labels = append(labels, l.IdentityLabel())
		}
		byLabel[l.IdentityLabel()] = append(byLabel[l.IdentityLabel()], l)
	}
	var parts [][]string
	for _, id := range labels {
		ls := byLabel[id]
		login := ls[0]
		login.Host = ""
		host := localHost
		if ls[0].Host != "" {
			host = tag(ls[0].Host)
		}
		row := []string{pal.bold.Render(host), pal.bold.Render(login.IdentityLabel())}
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

// shownOrder puts this host's rows first, then the others by the name the board shows, keeping each host's own order.
func shownOrder(rows []core.Limit, tag func(string) string) []core.Limit {
	out := slices.Clone(rows)
	shown := func(l core.Limit) string {
		if l.Host == "" {
			return ""
		}
		return tag(l.Host)
	}
	slices.SortStableFunc(out, func(a, b core.Limit) int { return strings.Compare(shown(a), shown(b)) })
	return out
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

// wrapRows is wrapItems for rows of cells: a row that fits no line of its own breaks between its cells, and a cell wider than a whole
// line breaks across lines, so nothing is cut.
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
		for j, cell := range splitWide(row, width-len(indent)-3) {
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

// splitWide breaks each cell wider than room into cells of at most room columns; room below 1 leaves them whole.
func splitWide(row []string, room int) []string {
	if room < 1 {
		return row
	}
	var out []string
	for _, cell := range row {
		if ansi.StringWidth(cell) > room {
			out = append(out, strings.Split(ansi.Hardwrap(cell, room, true), "\n")...)
		} else {
			out = append(out, cell)
		}
	}
	return out
}

// spendText is the spend at API prices and its approximate share of each current limit window.
func spendText(f core.Fleet, s core.Spend) string {
	if sh := f.Shares(s); sh != "" {
		return s.Dollars() + " " + pal.dim.Render(sh)
	}
	return s.Dollars()
}

func (m Model) loginName(h, id string) string { return core.LoginName(m.board.Profiles, h, id) }

func profileDisplay(h, id string) string {
	if id == "" || id == "default" {
		return h
	}
	return h + "/" + id
}
