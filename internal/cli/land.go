package cli

import (
	"fmt"

	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
	"github.com/spf13/cobra"
)

func landCmd(withApp runner) *cobra.Command {
	return &cobra.Command{
		Use:   "land [project]",
		Short: "Mark a project landed and close its tmux session; state is kept",
		Args:  cobra.MaximumNArgs(1),
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				a.project = args[0]
			}
			project, err := a.requireProject()
			if err != nil {
				return err
			}
			if err := a.unstageProject(project); err != nil {
				return err
			}
			session := tmux.SessionName(project)
			err = a.store.Land(project, func() error {
				if !a.tmux.HasSession(session) {
					return nil
				}
				return a.tmux.KillSession(session)
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s landed\n", project)
			return nil
		}),
	}
}

func kurganCmd(withApp runner) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "kurgan",
		Short: "List landed projects",
		Args:  cobra.NoArgs,
		RunE: withApp(func(a *app, cmd *cobra.Command, args []string) error {
			projects, err := a.store.Projects(store.ProjectLanded)
			if err != nil {
				return err
			}
			out := make([]projectJSON, 0, len(projects))
			for _, p := range projects {
				pj, err := a.describe(p, nil)
				if err != nil {
					return err
				}
				out = append(out, pj)
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), out)
			}
			for _, p := range out {
				fmt.Fprintf(cmd.OutOrStdout(), "%-24s landed %s  %d open  %s\n", p.Name, p.LandedAt, p.OpenItems, p.Cwd)
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}
