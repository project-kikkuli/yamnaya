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

// kill confirms what it ends, then kills the session, goal or chief ref names: a session by id, prefix or launch key, a goal or
// chief by id or name. With stop it only ends a session's agent, leaving its pane and worktree.
func kill(c *core.Core, ref string, yes, stop, tty bool, in io.Reader, out io.Writer) error {
	if !yes && !tty {
		return errors.New("ym kill asks y/n on a terminal; pass --yes to kill without asking")
	}
	ask := func(what string) bool {
		if yes {
			return true
		}
		fmt.Fprintf(out, "kill %s? y/n ", what)
		answer, _ := bufio.NewReader(in).ReadString('\n')
		if strings.TrimSpace(strings.ToLower(answer)) != "y" {
			fmt.Fprintln(out, "not killed")
			return false
		}
		return true
	}
	if host, rest, ok := c.RemoteRef(ref); ok {
		if stop {
			_, err := c.RunRemote(host, "kill", "--yes", "--stop", rest)
			return err
		}
		if !ask(rest + " on " + host) {
			return nil
		}
		msg, err := c.RunOrRelay(host, "kill", "--yes", rest)
		if msg != "" {
			fmt.Fprintln(out, msg)
		}
		return err
	}
	if !stop && c.OrgRef(ref) {
		b, err := c.Board()
		if err != nil {
			return err
		}
		m, targets, err := c.KillTargets(b, ref)
		if err != nil {
			return err
		}
		what := "goal " + m.GoalID + " with its manager, agents and verifier: " + c.Weigh(targets).String()
		if m.GoalID == "" {
			what = "chief " + m.Label() + " (retires it; its reports report one level up)"
		}
		if !ask(what) {
			return nil
		}
		msg, err := c.KillGoal(b, ref)
		if msg != "" {
			fmt.Fprintln(out, msg)
		}
		return err
	}
	s, err := killable(c, ref)
	if err != nil {
		return err
	}
	if stop {
		msg, err := c.Stop(s)
		if err == nil {
			fmt.Fprintln(out, msg)
		}
		return err
	}
	if !ask(s.Label() + ": " + c.Weigh([]core.Live{s}).String()) {
		return nil
	}
	msg, err := c.Kill(s)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, msg)
	return nil
}

// killable is the session ref names: by session id or prefix, or by a launch key.
func killable(c *core.Core, ref string) (core.Live, error) {
	s, err := c.Find(ref)
	if errors.Is(err, core.ErrNotFound) && c.Launches != nil {
		var matches []core.Launched
		for _, l := range c.Launches.Launched() {
			if l.Landed.IsZero() && (l.Key == ref || core.SessionMatches(l.Session, ref)) {
				matches = append(matches, l)
			}
		}
		if len(matches) > 1 {
			return s, fmt.Errorf("several launches match %q; use the full session id or launch key", ref)
		}
		for _, l := range matches {
			if l.Session != "" {
				s, err = c.Find(l.Session)
			}
			if errors.Is(err, core.ErrNotFound) {
				s = core.Live{Session: core.Session{ID: core.Dormant{Key: l.Key, Session: l.Session}.ID(), Harness: l.Harness}}
				err = nil
			}
		}
	}
	return s, err
}

func killCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var yes, stop bool
	cmd := &cobra.Command{
		Use: "kill <session|goal|chief>",
		Short: "End a session for good: its agent, its tmux pane and the worktree ym made for it, keeping work no remote holds for ym revive; " +
			"on a goal also its manager, agents and verifier; on a chief, retire it alone",
		Args: cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			return kill(e.core, args[0], yes, stop, stdinTTY(), cmd.InOrStdin(), cmd.OutOrStdout())
		}),
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "kill without asking")
	cmd.Flags().BoolVar(&stop, "stop", false, "only end the agent with SIGTERM, leaving its pane and worktree")
	cmd.Flags().MarkHidden("stop")
	return cmd
}
