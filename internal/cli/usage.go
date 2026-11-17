package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	var asJSON, refresh bool
	cmd := &cobra.Command{
		Use:   "usage",
		Short: "API-priced spend per session, today and this week, and subscription limits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var f core.Fleet
			var st usage.Stats
			var err error
			if refresh {
				f, st, err = scanned()
			} else {
				f, err = persistedUsage()
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"today": f.Today, "week": f.Week, "limits": f.Limits, "sessions": usage.Top(f, top)})
			}
			if refresh {
				fmt.Fprintf(out, "scan: %d files, %d changed, %.1f MB, %d responses, %s (parse %s, store %s)\n", st.Files, st.Changed, float64(st.Bytes)/1e6, st.Responses,
					st.Took.Round(time.Millisecond), st.Parse.Round(time.Millisecond), st.Commit.Round(time.Millisecond))
			}
			usage.Render(out, f, core.ThisHost(), top)
			return nil
		},
	}
	cmd.Flags().IntVar(&top, "top", 10, "sessions to list")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	cmd.Flags().BoolVar(&refresh, "refresh", false, "explicitly collect new transcript usage before reading")
	return cmd
}

func persistedUsage() (core.Fleet, error) {
	dir, err := stateDir()
	if err != nil {
		return core.Fleet{}, err
	}
	m := usage.NewReader(dir)
	defer m.Close()
	return m.Read(time.Now())
}

func limitsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "limits",
		Short: "Show Claude and Codex subscription limits",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			f, err := persistedUsage()
			if err != nil {
				return err
			}
			usage.RenderLimits(cmd.OutOrStdout(), f.Limits, f.At)
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
