package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func launchQueueCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: "launch", Short: "Inspect queued launches and recover blocked requests"}
	var asJSON bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, _ []string) error {
		pending, err := e.core.Store.PendingLaunches()
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(pending)
		}
		for _, p := range pending {
			reason := p.Reason
			if reason == "" {
				reason = p.Result
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s\n", p.ID, p.State, reason)
		}
		return nil
	})}
	list.Flags().BoolVar(&asJSON, "json", false, "print request identity, preserved launch, and blocking reason")
	retry := &cobra.Command{Use: "retry <id>", Short: "Retry a preserved request after inspecting prior native effects", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		message, err := e.core.RetryLaunch(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), message)
		return nil
	})}
	cmd.AddCommand(list, retry)
	return cmd
}
