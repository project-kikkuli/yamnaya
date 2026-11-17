package cli

import "github.com/spf13/cobra"

func actionAcceptCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "accept <id>", Short: "Acknowledge a supervisor action in its accepting session", Args: cobra.ExactArgs(1), RunE: with(func(e *env, _ *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		return e.core.AcceptAction(args[0], caller.Session)
	})}
}
