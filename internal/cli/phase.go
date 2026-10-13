package cli

import (
	"fmt"
	"io"

	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/spf13/cobra"
)

func printPhases(w io.Writer, p store.Project) {
	for i, ph := range p.Phases {
		mark := "  "
		switch {
		case i < p.PhaseIndex:
			mark = "x "
		case i == p.PhaseIndex:
			mark = "> "
		}
		fmt.Fprintf(w, "%s%s\n", mark, ph)
	}
}

func phaseCmd(withApp runner) *cobra.Command {
	cmd := &cobra.Command{Use: "phase", Short: "Show or advance the project's phases"}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <a,b,c>",
		Short: "Replace the phase list",
		Args:  cobra.ExactArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			p, err := a.store.SetPhases(project, splitPhases(args[0]))
			if err != nil {
				return err
			}
			printPhases(cmd.OutOrStdout(), p)
			return nil
		}),
	}, &cobra.Command{
		Use:   "next",
		Short: "Advance to the next phase",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			p, err := a.store.NextPhase(project)
			if err != nil {
				return err
			}
			printPhases(cmd.OutOrStdout(), p)
			return nil
		}),
	}, &cobra.Command{
		Use:   "show",
		Short: "Show phases; > marks the current one",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			p, err := a.store.Project(project)
			if err != nil {
				return err
			}
			printPhases(cmd.OutOrStdout(), p)
			return nil
		}),
	})
	return cmd
}
