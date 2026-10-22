package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/health"
)

// healthReport scans new transcript lines and rolls up the last weeks.
func healthReport(weeks int) (health.Report, health.Stats, error) {
	path, err := core.DefaultStorePath()
	if err != nil {
		return health.Report{}, health.Stats{}, err
	}
	h, err := health.Open(path)
	if err != nil {
		return health.Report{}, health.Stats{}, err
	}
	defer h.Close()
	st, err := h.Scan()
	if err != nil {
		return health.Report{}, st, err
	}
	r, err := h.Report(time.Now(), weeks)
	return r, st, err
}

// healthText is the weekly table the board shows on h.
func healthText() (string, error) {
	r, _, err := healthReport(8)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	health.Render(&b, r)
	b.WriteString("\n" + health.Legend)
	return b.String(), nil
}

func healthCmd() *cobra.Command {
	var weeks int
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "health",
		Short: "Weekly trend of item answer times, stale items, questions reprinted in chat, and agent time blocked on you",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			r, st, err := healthReport(weeks)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(r)
			}
			fmt.Fprintf(out, "scan: %d files, %d changed, %.1f MB, %s\n", st.Files, st.Changed, float64(st.Bytes)/1e6, st.Took.Round(time.Millisecond))
			health.Render(out, r)
			fmt.Fprint(out, "\n"+health.Legend)
			return nil
		},
	}
	cmd.Flags().IntVar(&weeks, "weeks", 8, "weeks to show")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON with every day")
	cmd.AddCommand(healthDriftCmd())
	return cmd
}
