package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func itemConsolidateCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var why string
	cmd := &cobra.Command{Use: "consolidate <copy> <original>", Short: "Merge a mistakenly refiled open request into its original, reopen and escalate the original, and retain both histories", Args: cobra.ExactArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		b, err := e.core.Board()
		if err != nil {
			return err
		}
		copyItem, err := e.item(b, args[0])
		if err != nil {
			return err
		}
		source, err := e.item(b, args[1])
		if err != nil {
			return err
		}
		if source.Host != copyItem.Host {
			return fmt.Errorf("both requests must be on the same host")
		}
		if source.Host != "" {
			host := source.Host
			source.Host, copyItem.Host = "", ""
			out, err := e.core.RunRemote(host, "item", "consolidate", copyItem.Key(), source.Key(), "--why", why)
			fmt.Fprint(cmd.OutOrStdout(), out)
			return err
		}
		it, err := e.core.Consolidate(copyItem.Key(), source.Key(), why)
		if err == nil {
			fmt.Fprintf(cmd.OutOrStdout(), "%s reopened for you; %s retained in history\n", it.Qualified(), copyItem.Qualified())
		}
		return err
	})}
	cmd.Flags().StringVar(&why, "why", "", "why the open request duplicates the original (required)")
	cmd.MarkFlagRequired("why")
	return cmd
}
