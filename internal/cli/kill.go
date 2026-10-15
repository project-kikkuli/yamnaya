package cli

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// stdinTTY reports whether ym can ask on stdin.
var stdinTTY = func() bool { return term.IsTerminal(os.Stdin.Fd()) }

// kill confirms, then ends the agent of the live session ref names: by session id or prefix, or by a launch key.
func kill(c *core.Core, ref string, yes, tty bool, in io.Reader, out io.Writer) error {
	if !yes && !tty {
		return errors.New("ym kill asks y/n on a terminal; pass --yes to kill without asking")
	}
	s, err := c.Find(ref)
	if errors.Is(err, core.ErrNotFound) && c.Launches != nil {
		for _, l := range c.Launches.Launched() {
			if l.Key == ref && l.Session != "" {
				s, err = c.Find(l.Session)
			}
		}
	}
	if err != nil {
		return err
	}
	if !yes {
		fmt.Fprintf(out, "kill %s? y/n ", s.Label())
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Fprintln(out, "not killed")
			return nil
		}
	}
	msg, err := c.Kill(s)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, msg)
	return nil
}

func killCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "kill <session>",
		Short: "End a session's claude or codex process with SIGTERM; its terminal, worktree and transcript stay, and a launched one can be revived",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			return kill(e.core, args[0], yes, stdinTTY(), cmd.InOrStdin(), cmd.OutOrStdout())
		}),
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "kill without asking")
	return cmd
}
