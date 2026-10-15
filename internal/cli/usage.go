package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

func stateDir() (string, error) {
	path, err := core.DefaultStorePath()
	if err != nil {
		return "", err
	}
	return filepath.Dir(path), nil
}

// scanned runs one full scan and hands back the fleet it built.
func scanned() (core.Fleet, usage.Stats, error) {
	dir, err := stateDir()
	if err != nil {
		return core.Fleet{}, usage.Stats{}, err
	}
	m := usage.New(dir)
	defer m.Close()
	now := time.Now()
	st, err := m.Scan(now)
	if err != nil {
		return core.Fleet{}, st, err
	}
	return m.Fleet(now), st, nil
}

func usageCmd() *cobra.Command {
	var top int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "API-priced spend per session, today and this week, and subscription limits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, st, err := scanned()
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			sessions := make([]core.SessionSpend, 0, len(f.Sessions))
			for _, s := range f.Sessions {
				sessions = append(sessions, s)
			}
			sort.Slice(sessions, func(i, j int) bool { return sessions[i].Total.USD > sessions[j].Total.USD })
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"today": f.Today, "week": f.Week, "limits": f.Limits, "sessions": sessions[:min(top, len(sessions))]})
			}
			fmt.Fprintf(out, "scan: %d files, %d changed, %.1f MB, %d responses, %s (parse %s, store %s)\n", st.Files, st.Changed, float64(st.Bytes)/1e6, st.Responses,
				st.Took.Round(time.Millisecond), st.Parse.Round(time.Millisecond), st.Commit.Round(time.Millisecond))
			fmt.Fprintf(out, "spend on %s: today %s   7d %s\n", core.ThisHost(), f.Today.Dollars(), f.Week.Dollars())
			writeLimits(out, f.Limits, f.At)
			var ours, theirs float64
			n := 0
			for _, s := range sessions {
				if s.Reported > 0 && s.Harness == "claude" {
					ours, theirs, n = ours+s.Total.USD, theirs+s.Reported, n+1
				}
			}
			if n > 0 {
				fmt.Fprintf(out, "claude cost-state: %d sessions, ym $%.2f vs reported $%.2f (%+.1f%%)\n", n, ours, theirs, 100*(ours-theirs)/theirs)
			}
			for _, s := range sessions[:min(top, len(sessions))] {
				t := s.Total.Tokens
				fmt.Fprintf(out, "%-6s %s  %s  in %s out %s cache read %s write 5m %s 1h %s", s.Harness, s.ID, s.Total.Dollars(),
					count(t.Input), count(t.Output), count(t.CacheRead), count(t.CacheWrite5m), count(t.CacheWrite1h))
				if s.Reported > 0 {
					fmt.Fprintf(out, "  reported $%.2f", s.Reported)
				}
				fmt.Fprintln(out)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&top, "top", 10, "sessions to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func limitsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "limits",
		Short: "Show Claude and Codex subscription limits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, _, err := scanned()
			if err != nil {
				return err
			}
			writeLimits(cmd.OutOrStdout(), f.Limits, f.At)
			return nil
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "record",
		Short: "Save the rate limits in Claude Code statusline JSON read from stdin",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := stateDir()
			if err != nil {
				return err
			}
			return usage.RecordClaudeLimits(dir, os.Stdin, time.Now())
		},
	})
	return cmd
}

func writeLimits(out io.Writer, ls []core.Limit, now time.Time) {
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
