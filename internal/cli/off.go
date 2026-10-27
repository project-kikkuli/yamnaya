package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// readsOnly are the commands a session ym is off for may still run: they show, and deliver or change nothing, besides ym on and
// off themselves and the board.
var readsOnly = map[string]bool{
	"ym": true, "ym on": true, "ym off": true, "ym serve": true, "ym board": true, "ym sessions": true, "ym launched": true,
	"ym waiting": true, "ym item list": true, "ym item show": true, "ym goal list": true, "ym goal show": true, "ym goal feed": true,
	"ym goal context": true, "ym drift": true, "ym profiles": true, "ym phase show": true,
}

// refuseOff fails every other command run from inside a session ym is off for.
func (e *env) refuseOff(cmd *cobra.Command) error {
	if cmd.Hidden || readsOnly[cmd.CommandPath()] || !e.core.CallerOff(os.Getpid(), os.Getenv) {
		return nil
	}
	return core.ErrOff
}

func offCmds(with func(runFunc) func(*cobra.Command, []string) error) []*cobra.Command {
	toggle := func(off bool) *cobra.Command {
		use, short := "off [<session|goal|manager|chief>]", "Turn ym off for this session, or for a session, goal or chief: ym delivers nothing to it and its ym commands refuse"
		if !off {
			use, short = "on [<session|goal|manager|chief>]", "Turn ym back on, handing each session it reaches again what waited for it, as one message"
		}
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.MaximumNArgs(1),
			RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
				var t core.OffTarget
				if len(args) == 1 {
					var err error
					if t, err = e.core.OffTargetOf(args[0]); err != nil {
						return err
					}
				} else {
					cl, err := e.caller()
					if err != nil {
						return err
					}
					t = core.OffTarget{Session: cl.Session}
				}
				b, err := e.core.Board()
				if err != nil {
					return err
				}
				msg, err := e.core.SetOff(b, t, off)
				if msg != "" {
					fmt.Fprintln(cmd.OutOrStdout(), msg)
				}
				return err
			}),
		}
	}
	return []*cobra.Command{toggle(true), toggle(false)}
}
