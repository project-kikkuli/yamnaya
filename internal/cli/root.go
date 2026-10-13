// Package cli wires the adapters into core and exposes the ym commands.
package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/host/iterm"
	"github.com/project-kikkuli/yamnaya/internal/host/terminal"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/tui"
)

// Wire builds a core with every adapter and the store at path.
func Wire(path string) (*core.Core, error) {
	st, err := core.OpenStore(path)
	if err != nil {
		return nil, err
	}
	return &core.Core{
		Store:     st,
		Harnesses: []core.Harness{claude.New(), codex.New()},
		Hosts:     []core.Host{tmux.New(), iterm.New(), terminal.New()},
		Procs:     proc.List,
	}, nil
}

type env struct {
	core    *core.Core
	session string
	project string
}

type runFunc func(e *env, cmd *cobra.Command, args []string) error

func NewRoot() *cobra.Command {
	e := &env{}
	root := &cobra.Command{
		Use:           "ym",
		Short:         "One screen for every Claude Code and Codex session and what needs you",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().StringVar(&e.session, "session", "", "act as this session id instead of the calling agent's")
	root.PersistentFlags().StringVarP(&e.project, "project", "p", "", "project key, name or session id (default: the calling agent's project)")
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error {
			path, err := core.DefaultStorePath()
			if err != nil {
				return err
			}
			c, err := Wire(path)
			if err != nil {
				return err
			}
			defer c.Store.Close()
			e.core = c
			return run(e, cmd, args)
		}
	}
	root.RunE = with(func(e *env, cmd *cobra.Command, args []string) error { return tui.Run(e.core) })
	root.AddCommand(
		itemCmd(with),
		phaseCmd(with),
		projectCmd(with),
		linkCmd(with),
		sessionsCmd(with),
		openCmd(with),
		switchCmd(with),
		installCmd(),
	)
	return root
}

// caller is the session ym was run from: --session, else the nearest claude or codex ancestor process.
func (e *env) caller() (core.Caller, error) {
	if e.session != "" {
		return core.Caller{Session: e.session}, nil
	}
	c, err := e.core.Identify(os.Getpid())
	if errors.Is(err, core.ErrNotFound) {
		return c, fmt.Errorf("%w; run ym from inside a claude or codex session, or pass --session", err)
	}
	return c, err
}

// projectKey resolves --project, else the caller's project.
func (e *env) projectKey(b core.Board) (string, error) {
	if e.project != "" {
		return b.ProjectKey(e.project)
	}
	c, err := e.caller()
	if err != nil {
		return "", err
	}
	return b.ProjectOf(c.Session), nil
}
