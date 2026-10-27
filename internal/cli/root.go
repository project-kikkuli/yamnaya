// Package cli wires the adapters into core and exposes the ym commands.
package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/gh"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/health"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/progress"
	"github.com/project-kikkuli/yamnaya/internal/slot"
	"github.com/project-kikkuli/yamnaya/internal/tui"
	"github.com/project-kikkuli/yamnaya/internal/usage"
)

// Wire builds a core with every adapter and the store at path.
func Wire(path string) (*core.Core, error) {
	st, err := core.OpenStore(path)
	if err != nil {
		return nil, err
	}
	cl := &profileHarness{name: claude.Name, dir: filepath.Dir(path), cache: st}
	cx := &profileHarness{name: codex.Name, dir: filepath.Dir(path), cache: st}
	if _, err := profile.Load(filepath.Dir(path)); err != nil {
		st.Close()
		return nil, err
	}
	srv := tmux.NewServer()
	c := &core.Core{
		ProfileList: func() []core.LoginProfile {
			ps, _ := profile.Load(filepath.Dir(path))
			var out []core.LoginProfile
			limits := profileLimits(filepath.Dir(path))
			for _, p := range ps {
				out = append(out, core.LoginProfile{Host: p.Host, State: profileStatus(p, limits), Harness: p.Harness, ID: p.ID, Label: p.Label, Account: p.Account})
			}
			return out
		},
		Store:     st,
		Harnesses: []core.Harness{cl, cx},
		Hosts:     []core.Host{tmux.New(), srv},
		Procs:     proc.List,
		Git:       core.ReadGit,
		PRs:       gh.New(),
		Landings:  gh.New(),
		Port:      port,
		Roots:     core.RepoRoots,
		Ask:       health.Addressed,
		Measure:   func(p core.Project) (float64, bool) { return progress.Of(p).Fraction() },
		StateDir:  filepath.Dir(path),
		Notify:    desktopNotify,
		RunSelf:   runSelf,
		Slots:     slot.Machine,
		CPU:       slot.CPU,
		Mem:       slot.Mem,
	}
	if l, err := newLauncher(); err == nil {
		l.core, l.srv = c, srv
		c.Launches = l
		c.PreparePort = l.prepareProfilePort
		c.BackgroundSend = func(s core.Live, text string) error { _, err := l.sendFrom(s, text, false); return err }
	}
	return c, c.ScopeItems()
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
			if cmd.Name() != "serve" {
				keepServing(cmd.ErrOrStderr())
			}
			return run(e, cmd, args)
		}
	}
	remotes := func(show bool) func(runFunc) func(*cobra.Command, []string) error {
		return func(run runFunc) func(*cobra.Command, []string) error {
			return with(func(e *env, cmd *cobra.Command, args []string) error {
				if err := e.readRemotes(remoteWait, show); err != nil {
					return err
				}
				return run(e, cmd, args)
			})
		}
	}
	withHosts, routed := remotes(true), remotes(false)
	root.RunE = with(func(e *env, cmd *cobra.Command, args []string) error {
		if err := e.readRemotes(0, true); err != nil {
			return err
		}
		adoptChildren()
		keepDefault(cmd.ErrOrStderr())
		if err := ensureWorkService(); err != nil {
			return err
		}
		e.core.KeepAlive = ensureWorkService
		dir, err := stateDir()
		if err != nil {
			return err
		}
		m := usage.New(dir)
		defer m.Close()
		e.core.Meter = m
		e.core.Health = healthText
		return tui.Run(e.core)
	})
	root.AddCommand(
		serveCmd(with),
		docsCmd(with),
		serviceCmd(),
		slotCmd(),
		profilesCmd(with),
		goalCmd(with, withHosts),
		chiefCmd(with, withHosts),
		itemCmd(with, routed),
		phaseCmd(with),
		vibeCmd(with),
		projectCmd(with),
		linkCmd(with),
		sessionsCmd(withHosts),
		boardCmd(with),
		openCmd(routed),
		switchCmd(routed),
		newCmd(with),
		reviveCmd(routed),
		gcCmd(with),
		killCmd(routed),
		launchedCmd(with),
		linkLaunchCmd(with),
		adoptCmd(with),
		sendCmd(routed),
		waitingCmd(withHosts),
		sendHoldCmd(with),
		resumeWatchCmd(),
		installCmd(),
		updateCmd(runGo),
		usageCmd(),
		healthCmd(),
		limitsCmd(),
		remoteCmd(),
		relayCmd(with),
	)
	return root
}

// remoteWait is how long a one-shot command waits for a host ym remote reached; the board never waits.
const remoteWait = 8 * time.Second

// caller uses --session, else the harness's calling session from ancestry and Codex's thread environment.
func (e *env) caller() (core.Caller, error) {
	if e.session != "" {
		return e.core.CallerOf(e.session)
	}
	c, err := e.core.Calling(os.Getpid(), os.Getenv)
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
