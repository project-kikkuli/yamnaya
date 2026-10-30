package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func printPhases(w io.Writer, p core.Phases) {
	for i, name := range p.Names {
		mark := "  "
		switch {
		case i < p.Current:
			mark = "x "
		case i == p.Current:
			mark = "> "
		}
		fmt.Fprintf(w, "%s%s\n", mark, name)
	}
}

func phaseCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: "phase", Short: "Show or advance the calling session's project phases"}
	run := func(f func(e *env, key string, args []string) (core.Phases, error)) func(*cobra.Command, []string) error {
		return with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			key, err := e.projectKey(b)
			if err != nil {
				return err
			}
			p, err := f(e, key, args)
			if err != nil {
				return err
			}
			printPhases(cmd.OutOrStdout(), p)
			return nil
		})
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "set <a,b,c>",
		Short: "Replace the phase list",
		Args:  cobra.ExactArgs(1),
		RunE: run(func(e *env, key string, args []string) (core.Phases, error) {
			var names []string
			for _, n := range strings.Split(args[0], ",") {
				if n = strings.TrimSpace(n); n != "" {
					names = append(names, n)
				}
			}
			return e.core.Store.SetPhases(key, names)
		}),
	}, &cobra.Command{
		Use:   "next",
		Short: "Advance to the next phase",
		Args:  cobra.NoArgs,
		RunE:  run(func(e *env, key string, _ []string) (core.Phases, error) { return e.core.Store.NextPhase(key) }),
	}, &cobra.Command{
		Use:   "show",
		Short: "Show phases; > marks the current one",
		Args:  cobra.NoArgs,
		RunE:  run(func(e *env, key string, _ []string) (core.Phases, error) { return e.core.Store.Phases(key) }),
	})
	return cmd
}

func projectCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Rename, merge or split inferred projects"}
	resolve := func(f func(e *env, b core.Board, args []string) (string, error)) func(*cobra.Command, []string) error {
		return with(func(e *env, cmd *cobra.Command, args []string) error {
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			msg, err := f(e, b, args)
			if err == nil {
				fmt.Fprintln(cmd.OutOrStdout(), msg)
			}
			return err
		})
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "rename <project> <name>",
		Short: "Name a project; an empty name restores the inferred one",
		Args:  cobra.MinimumNArgs(1),
		RunE: resolve(func(e *env, b core.Board, args []string) (string, error) {
			key, err := b.ProjectKey(args[0])
			if err != nil {
				return "", err
			}
			name := strings.Join(args[1:], " ")
			return key + " named " + name, e.core.Store.Rename(key, name)
		}),
	}, &cobra.Command{
		Use:   "merge <project> <into>",
		Short: "Show a project as part of another",
		Args:  cobra.ExactArgs(2),
		RunE: resolve(func(e *env, b core.Board, args []string) (string, error) {
			from, err := b.ProjectKey(args[0])
			if err != nil {
				return "", err
			}
			into, err := b.ProjectKey(args[1])
			if err != nil {
				return "", err
			}
			return from + " merged into " + into, e.core.Store.Merge(from, into)
		}),
	}, &cobra.Command{
		Use:   "split <project>",
		Short: "Undo every merge into or out of a project",
		Args:  cobra.ExactArgs(1),
		RunE: resolve(func(e *env, b core.Board, args []string) (string, error) {
			key, err := b.ProjectKey(args[0])
			if err != nil {
				key = args[0]
			}
			n, err := e.core.Store.Split(key)
			if err == nil && n == 0 {
				err = fmt.Errorf("%s has no merges", args[0])
			}
			return fmt.Sprintf("%s: undid %d merges", key, n), err
		}),
	})
	return cmd
}

func linkCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var parent string
	cmd := &cobra.Command{
		Use:     "spawn <child-session-id>",
		Aliases: []string{"link"},
		Short:   "Record that the calling session spawned another, so both show as one project",
		Args:    cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if parent == "" {
				c, err := e.caller()
				if err != nil {
					return err
				}
				parent = c.Session
			}
			from, err := e.core.CallerOf(parent)
			if err != nil {
				return err
			}
			child, err := e.core.CallerOf(args[0])
			if err != nil {
				return err
			}
			if err := e.core.RegisterSpawn(child.Session, from.Session); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s spawned by %s\n", child.Session, from.Session)
			return nil
		}),
	}
	cmd.Flags().StringVar(&parent, "parent", "", "parent session id (default: the calling session)")
	return cmd
}

func openCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var pick int
	var noBoard bool
	cmd := &cobra.Command{
		Use:   "open <session-id|item-id>",
		Short: "Focus the terminal running a session, or print its resume command when no terminal can be reached",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			show := e.attach
			if noBoard {
				show = (*exec.Cmd).Run
			}
			if ok, err := e.openLaunched(out, args[0], show); ok || err != nil {
				return err
			}
			b, err := e.core.Board()
			if err != nil {
				return err
			}
			key, harness := args[0], ""
			if it, err := e.item(b, args[0]); err == nil {
				key, harness = it.Session, it.Harness
			} else if s, err := sessionByPrefix(b, args[0]); err == nil {
				key, harness = s.Key(), s.Harness
			} else if !errors.Is(err, core.ErrNotFound) {
				return err
			}
			ts := e.core.Targets(b, key, harness)
			if len(ts) == 0 {
				if s, err := e.core.Find(args[0]); err == nil && s.Remote != "" {
					ts = []core.Target{{Session: s.Session, Remote: s.Remote}}
				}
			}
			if len(ts) == 0 {
				return fmt.Errorf("no session %q", args[0])
			}
			if len(ts) > 1 && pick == 0 {
				fmt.Fprintf(out, "%s runs in %d processes; pass --pick N:\n", core.Short(key), len(ts))
				for i, t := range ts {
					fmt.Fprintf(out, "  %d  %s\n", i+1, t)
				}
				return nil
			}
			t := ts[0]
			if pick > 0 {
				if pick > len(ts) {
					return fmt.Errorf("--pick %d: only %d processes", pick, len(ts))
				}
				t = ts[pick-1]
			}
			c, err := e.core.AttachCmd(t)
			if err != nil {
				return err
			}
			if c != nil {
				return show(c)
			}
			err = e.core.Focus(t)
			if errors.Is(err, core.ErrNoHost) {
				fmt.Fprintln(out, t.Advice())
				return nil
			}
			if err == nil {
				fmt.Fprintf(out, "opened %s\n", t)
			}
			return err
		}),
	}
	cmd.Flags().IntVar(&pick, "pick", 0, "which process to open when a session runs in several")
	cmd.Flags().BoolVar(&noBoard, "no-board", false, "after detaching, return to the caller instead of showing the board")
	return cmd
}

func sessionByPrefix(b core.Board, ref string) (core.Live, error) {
	for _, s := range b.Sessions {
		key := s.Ref()
		if s.Remote != "" {
			key = core.RemoteKey(s.Remote, key)
		}
		if key == ref && key != s.Key() {
			return s, nil
		}
	}
	var ids []string
	for _, s := range b.Sessions {
		ids = append(ids, s.Key())
	}
	id, err := core.SessionRef(ref, ids)
	if err != nil {
		return core.Live{}, err
	}
	var found core.Live
	have := false
	for _, s := range b.Sessions {
		if s.Key() != id {
			continue
		}
		if have && (found.Ref() != s.Ref() || found.Remote != s.Remote) {
			return core.Live{}, fmt.Errorf("%w %q: use a host/profile-qualified session", core.ErrAmbiguous, ref)
		}
		found, have = s, true
	}
	return found, nil
}
