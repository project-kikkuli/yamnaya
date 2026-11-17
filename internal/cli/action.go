package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func actionCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: "action", Short: "Inspect and resolve durable supervisor actions"}
	var asJSON bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, _ []string) error {
		items, err := e.core.Store.Activations()
		if err != nil {
			return err
		}
		if asJSON {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(items)
		}
		for _, a := range items {
			if a.State != "done" && a.State != "superseded" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s %s %s\n", a.ID, a.Recipient, a.Kind, a.State, a.Error)
			}
		}
		return nil
	})}
	list.Flags().BoolVar(&asJSON, "json", false, "print structured action records")
	show := &cobra.Command{Use: "show <id>", Args: cobra.ExactArgs(1), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		items, err := e.core.Store.Activations()
		if err != nil {
			return err
		}
		for _, a := range items {
			if a.ID == args[0] {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(a)
			}
		}
		return fmt.Errorf("supervisor action %s not found", args[0])
	})}
	var result string
	complete := &cobra.Command{Use: "complete <id>", Args: cobra.ExactArgs(1), RunE: with(func(e *env, _ *cobra.Command, args []string) error {
		caller, err := e.caller()
		if err != nil {
			return err
		}
		return e.core.CompleteAction(args[0], caller.Session, result)
	})}
	complete.Flags().StringVar(&result, "result", "", "what this action changed")
	_ = complete.MarkFlagRequired("result")
	retry := &cobra.Command{Use: "retry <id>", Short: "Explicitly retry after inspecting prior submission effects", Args: cobra.ExactArgs(1), RunE: with(func(e *env, _ *cobra.Command, args []string) error {
		return e.core.RetryAction(args[0])
	})}
	cmd.AddCommand(list, show, complete, retry, actionAcceptCmd(with))
	return cmd
}
