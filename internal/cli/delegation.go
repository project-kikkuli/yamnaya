package cli

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/project-kikkuli/yamnaya/internal/work"
	"github.com/spf13/cobra"
)

func delegationCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var owner, purpose, authority, next string
	cmd := &cobra.Command{Use: "delegate <parent> <name>", Short: "Give an agent one accountable child outcome", Args: cobra.ExactArgs(2), RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		p, err := e.core.Store.Work.Find(args[0])
		if err != nil {
			return err
		}
		a, err := e.core.Store.Work.Agent(owner)
		if err != nil {
			return err
		}
		if actor := e.actor(); actor != "owner" && actor != p.Owner {
			return fmt.Errorf("only the accountable agent or owner delegates this outcome")
		}
		w, err := e.core.Store.Work.Ensure("delegation/"+uuid.NewString(), p.Repo, args[1])
		if err != nil {
			return err
		}
		for _, event := range []work.Event{{Type: "purpose", Text: purpose}, {Type: "authority", Text: authority}, {Type: "parent", Target: p.ID}, {Type: "plan", Text: next}, {Type: "owner", Target: a.ID}} {
			event.Work, event.Expected, event.Actor = w.ID, w.Revision, e.actor()
			w, err = e.core.Store.Work.Commit(event)
			if err != nil {
				return err
			}
		}
		if err = e.core.Store.Work.AttachDefaults(w); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), w.ID)
		return nil
	})}
	cmd.Flags().StringVar(&owner, "agent", "", "accountable agent")
	cmd.Flags().StringVar(&purpose, "purpose", "", "outcome and completion criteria")
	cmd.Flags().StringVar(&authority, "authority", "", "delegated actions and limits")
	cmd.Flags().StringVar(&next, "next", "", "first independent action")
	for _, name := range []string{"agent", "purpose", "authority", "next"} {
		cmd.MarkFlagRequired(name)
	}
	return cmd
}
