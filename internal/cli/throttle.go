package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func throttleCmds(with func(runFunc) func(*cobra.Command, []string) error) []*cobra.Command {
	toggle := func(on bool) *cobra.Command {
		use, short := "throttle [<session|goal|manager|chief>]", "Pause every ym agent on this host, or those under a session, goal or chief: stop each agent and everything it runs (SIGSTOP) until ym unthrottle"
		if !on {
			use, short = "unthrottle [<session|goal|manager|chief>]", "Resume the agents ym throttle paused (SIGCONT), all of them or those under a session, goal or chief"
		}
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.MaximumNArgs(1),
			RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
				var t *core.OffTarget
				if len(args) == 1 {
					ref, err := e.core.OffTargetOf(args[0])
					if err != nil {
						return err
					}
					t = &ref
				}
				b, err := e.core.Board()
				if err != nil {
					return err
				}
				msg, err := e.core.Throttle(b, t, on)
				if msg != "" {
					fmt.Fprintln(cmd.OutOrStdout(), msg)
				}
				return err
			}),
		}
	}
	return []*cobra.Command{toggle(true), toggle(false)}
}
