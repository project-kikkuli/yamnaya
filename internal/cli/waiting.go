package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type waitingJSON struct {
	Session string `json:"session"`
	Harness string `json:"harness"`
	Title   string `json:"title"`
	Project string `json:"project"`
	Host    string `json:"host"`
	Since   string `json:"since"`
	Line    string `json:"line"`
	Reply   string `json:"reply"`
}

func waitingCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "waiting",
		Short: "List idle sessions whose last reply asks you, with no open item; answer one with ym send",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			out := []waitingJSON{}
			for _, n := range b.Needs {
				if !n.Waiting() {
					continue
				}
				s := n.Session
				name := core.Short(n.Project)
				if p, ok := b.Project(n.Project); ok {
					name = p.Name
				}
				out = append(out, waitingJSON{Session: s.Key(), Harness: s.Harness, Title: s.Title, Project: name, Host: s.Location(),
					Since: s.Since.UTC().Format("2006-01-02T15:04:05Z"), Line: core.LastLine(n.Reply), Reply: n.Reply})
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			for _, w := range out {
				fmt.Fprintf(cmd.OutOrStdout(), "%-8s %-6s %-24s waiting: %s\n", core.Short(w.Session), w.Harness, w.Project, w.Line)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
