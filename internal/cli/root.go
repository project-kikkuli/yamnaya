package cli

import (
	"github.com/spf13/cobra"
)

func NewRoot() *cobra.Command {
	var project string
	root := &cobra.Command{
		Use:           "ym",
		Short:         "A tmux command center for Claude Code and Codex sessions",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVarP(&project, "project", "p", "", "project (default $YM_PROJECT)")
	withApp := func(run func(a *app, cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			a, err := openApp(project)
			if err != nil {
				return err
			}
			defer a.close()
			return run(a, cmd, args)
		}
	}
	root.Args = cobra.NoArgs
	root.RunE = withApp(runHome)
	root.AddCommand(
		newCmd(withApp),
		spawnCmd(withApp),
		itemCmd(withApp),
		phaseCmd(withApp),
		landCmd(withApp),
		kurganCmd(withApp),
		lsCmd(withApp),
		jumpCmd(withApp),
		reviveCmd(withApp),
		switchCmd(withApp),
		installCmd(),
		adoptCmd(withApp),
		nextCmd(withApp),
		boardCmd(withApp),
		homeCmd(withApp),
		restCmd(),
	)
	return root
}

type runner func(run func(a *app, cmd *cobra.Command, args []string) error) func(*cobra.Command, []string) error
