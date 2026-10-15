package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func printVibe(w io.Writer, v core.Vibe) {
	line := v.At.Local().Format("Jan 2 15:04") + "  " + v.Place
	if v.Why != "" {
		line += "  " + v.Why
	}
	if v.Session != "" {
		line += "  (" + v.Harness + " " + core.Short(v.Session) + ")"
	}
	fmt.Fprintln(w, line)
}

func vibeCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var why string
	cmd := &cobra.Command{
		Use:   "vibe [place]",
		Short: "Say how the calling session's mission is going, as a place from North Dakota (rough) to Hawaii (great); with no place, show its vibes",
		Args:  cobra.MaximumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 1 {
				v := core.Vibe{Place: args[0], Why: why}
				c, err := e.caller()
				if err == nil {
					v.Session, v.Harness = c.Session, c.Harness
				} else if e.project == "" {
					return err
				}
				if e.project != "" {
					b, err := e.core.Board()
					if err != nil {
						return err
					}
					if v.Target, err = b.ProjectKey(e.project); err != nil {
						return err
					}
				}
				if v, err = e.core.Store.AddVibe(v); err == nil {
					printVibe(out, v)
				}
				return err
			}
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			key, err := e.projectKey(b)
			if err != nil {
				return err
			}
			all, err := e.core.Store.Vibes()
			if err != nil {
				return err
			}
			vs := b.VibesOf(all, key)
			if len(vs) == 0 {
				fmt.Fprintf(out, "no vibe yet for %s\n", key)
			}
			for _, v := range vs[:min(len(vs), 10)] {
				printVibe(out, v)
			}
			return nil
		}),
	}
	cmd.Flags().StringVar(&why, "why", "", "one line on why")
	return cmd
}
