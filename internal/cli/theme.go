package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/scene"
	_ "github.com/project-kikkuli/yamnaya/internal/scene/themes"
)

func themeCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "theme [name]",
		Short: "Show the board's themes, or set the one it draws behind it; L on the board cycles them",
		Args:  cobra.MaximumNArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			var names []string
			for _, t := range scene.Themes() {
				names = append(names, t.Name())
			}
			if len(args) == 1 {
				if scene.Lookup(args[0]).Name() != args[0] {
					return fmt.Errorf("no theme %q; themes: %s", args[0], strings.Join(names, ", "))
				}
				if err := e.core.Store.SetTheme(args[0]); err != nil {
					return err
				}
			}
			cur, err := e.core.Store.Theme()
			if err != nil {
				return err
			}
			cur = scene.Lookup(cur).Name()
			for _, n := range names {
				mark := "  "
				if n == cur {
					mark = "* "
				}
				fmt.Fprintln(cmd.OutOrStdout(), mark+n)
			}
			return nil
		}),
	}
}
