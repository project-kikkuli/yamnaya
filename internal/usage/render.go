package usage

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Top lists the top sessions of f by spend, most first.
func Top(f core.Fleet, n int) []core.SessionSpend {
	out := make([]core.SessionSpend, 0, len(f.Sessions))
	for _, s := range f.Sessions {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Total.USD > out[j].Total.USD })
	return out[:min(n, len(out))]
}

// Render writes the spend on host today and this week, the limits, and the top sessions by spend.
func Render(out io.Writer, f core.Fleet, host string, top int) {
	fmt.Fprintf(out, "spend on %s: today %s   7d %s\n", host, f.Today.Dollars(), f.Week.Dollars())
	RenderLimits(out, f.Limits, f.At)
	var ours, theirs float64
	n := 0
	for _, s := range Top(f, len(f.Sessions)) {
		if s.Reported > 0 && s.Harness == "claude" {
			ours, theirs, n = ours+s.Total.USD, theirs+s.Reported, n+1
		}
	}
	if n > 0 {
		fmt.Fprintf(out, "claude cost-state: %d sessions, ym $%.2f vs reported $%.2f (%+.1f%%)\n", n, ours, theirs, 100*(ours-theirs)/theirs)
	}
	for _, s := range Top(f, top) {
		t := s.Total.Tokens
		fmt.Fprintf(out, "%-6s %s  %s  in %s out %s cache read %s write 5m %s 1h %s", s.Harness, s.ID, s.Total.Dollars(),
			count(t.Input), count(t.Output), count(t.CacheRead), count(t.CacheWrite5m), count(t.CacheWrite1h))
		if s.Reported > 0 {
			fmt.Fprintf(out, "  reported $%.2f", s.Reported)
		}
		fmt.Fprintln(out)
	}
}

func RenderLimits(out io.Writer, ls []core.Limit, now time.Time) {
	if len(ls) == 0 {
		fmt.Fprintln(out, "limits: none reported")
		return
	}
	for _, l := range ls {
		fmt.Fprintf(out, "limit: %s %s %.0f%% resets in %s (seen %s ago)", l.Harness, l.Label(), l.Used, core.Span(l.ResetsAt.Sub(now)), core.Span(now.Sub(l.Seen)))
		if l.Plan != "" {
			fmt.Fprintf(out, " plan %s", l.Plan)
		}
		fmt.Fprintln(out)
	}
}

func count(n int64) string {
	switch {
	case n >= 1e9:
		return fmt.Sprintf("%.1fB", float64(n)/1e9)
	case n >= 1e6:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1e3:
		return fmt.Sprintf("%.0fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}
