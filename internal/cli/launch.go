package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/tui"
)

type launcher struct {
	srv     *tmux.Host
	records launch.Records
	conf    string
}

func newLauncher() (launcher, error) {
	path, err := core.DefaultStorePath()
	if err != nil {
		return launcher{}, err
	}
	dir := filepath.Dir(path)
	l := launcher{srv: tmux.NewServer(), records: launch.Records{Dir: filepath.Join(dir, "launches")}, conf: filepath.Join(dir, "tmux.conf")}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return l, err
	}
	return l, os.WriteFile(l.conf, []byte(tmux.ServerConf), 0o644)
}

// bin is the absolute path of a harness binary, so the ym server needs no PATH of its own.
func bin(harnessName string) (string, error) {
	name := map[string]string{claude.Name: claude.New().Bin, codex.Name: codex.New().Bin}[harnessName]
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH: %w", name, err)
	}
	return path, nil
}

func startArgv(rec launch.Record) ([]string, error) {
	b, err := bin(rec.Harness)
	if err != nil {
		return nil, err
	}
	if rec.Harness == claude.Name {
		return []string{b, "--session-id", rec.Session, rec.Prompt}, nil
	}
	return append(append([]string{b}, codexDirs(rec)...), rec.Prompt), nil
}

func resumeArgv(rec launch.Record) ([]string, error) {
	b, err := bin(rec.Harness)
	if err != nil {
		return nil, err
	}
	if rec.Harness == claude.Name {
		return []string{b, "--resume", rec.Session}, nil
	}
	return append(append([]string{b, "resume"}, codexDirs(rec)...), rec.Session), nil
}

// codexDirs lets Codex's sandbox write ym's state, so ym item works, and the repository's git dir, so the worktree can commit.
// A state dir set by $YAMNAYA_STATE_DIR is passed to Codex's shells, which drop most of the environment.
func codexDirs(rec launch.Record) []string {
	var args []string
	if path, err := core.DefaultStorePath(); err == nil {
		args = append(args, "--add-dir", filepath.Dir(path))
	}
	if dir := os.Getenv("YAMNAYA_STATE_DIR"); dir != "" {
		args = append(args, "-c", "shell_environment_policy.set.YAMNAYA_STATE_DIR="+strconv.Quote(dir))
	}
	if rec.Worktree.GitDir != "" {
		args = append(args, "--add-dir", rec.Worktree.GitDir)
	}
	return args
}

// settle fills in a Codex session id from Codex's own thread table once Codex has written it.
func (l launcher) settle(rec launch.Record) launch.Record {
	if rec.Session != "" || rec.Harness != codex.Name {
		return rec
	}
	if id := codex.New().LatestIn(rec.Worktree.Path, rec.Started); id != "" {
		rec.Session = id
		l.records.Save(rec)
	}
	return rec
}

func (l launcher) find(ref string) (launch.Record, error) {
	rec, ok := l.records.Find(ref)
	if !ok {
		return rec, fmt.Errorf("no session launched by ym matches %q; see ym launched", ref)
	}
	if head := launch.Head(rec.Worktree.Path); head != "" && head != rec.Head {
		rec.Head = head
		l.records.Save(rec)
	}
	return l.settle(rec), nil
}

// attach shows a launched session in this terminal, then returns to the board.
func (e *env) attach(cmd *exec.Cmd) error {
	if err := cmd.Run(); err != nil {
		return err
	}
	return tui.Run(e.core)
}

// openLaunched opens a session by its launch key; ok is false when ref names no launch.
func (e *env) openLaunched(w io.Writer, ref string) (bool, error) {
	l, err := newLauncher()
	if err != nil {
		return false, err
	}
	rec, ok := l.records.Find(ref)
	if !ok {
		return false, nil
	}
	pane, st := l.state(rec)
	if st != "running" {
		fmt.Fprintf(w, "%s %s; ym revive %s\n", rec.Key, st, rec.Key)
		return true, nil
	}
	t := core.Target{Session: core.Session{Harness: rec.Harness, ID: rec.Session}, Handle: &core.Handle{Host: l.srv.Name(), ID: pane.ID, Label: pane.Session}}
	if c := e.core.AttachCmd(t); c != nil {
		return true, e.attach(c)
	}
	return true, e.core.Focus(t)
}

func newCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var useCodex bool
	var repo, slug string
	cmd := &cobra.Command{
		Use:   "new [--codex] [--repo PATH] [--slug S] <prompt>",
		Short: "Start claude (or codex) on a prompt in a new worktree, in ym's own tmux server",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, err := newLauncher()
			if err != nil {
				return err
			}
			if repo == "" {
				if repo, err = os.Getwd(); err != nil {
					return err
				}
			}
			if slug == "" {
				slug = launch.Slug(args[0])
			}
			rec := launch.Record{Harness: claude.Name, Prompt: args[0], Started: time.Now()}
			if useCodex {
				rec.Harness = codex.Name
			} else {
				rec.Session = uuid.NewString()
			}
			if _, err := bin(rec.Harness); err != nil {
				return err
			}
			if c, err := e.caller(); err == nil {
				rec.Parent = c.Session
			} else if !errors.Is(err, core.ErrNotFound) {
				return err
			}
			if rec.Worktree, err = launch.Create(repo, slug); err != nil {
				return err
			}
			rec.Key, rec.Head = filepath.Base(rec.Worktree.Path), launch.Head(rec.Worktree.Path)
			argv, err := startArgv(rec)
			if err != nil {
				return err
			}
			if err := launch.Trust(rec.Harness, rec.Worktree.Path); err != nil {
				return err
			}
			if err := l.records.Save(rec); err != nil {
				return err
			}
			name := tmux.SessionName(rec.Key)
			if _, err := l.srv.Start(l.conf, name, rec.Worktree.Path, argv); err != nil {
				return err
			}
			if rec.Parent != "" {
				if rec.Session != "" {
					if err := e.core.Store.Link(rec.Session, rec.Parent); err != nil {
						return err
					}
				} else if err := detach(rec.Key); err != nil {
					return err
				}
			}
			printLaunch(cmd.OutOrStdout(), rec, name, l.srv.Socket)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&useCodex, "codex", false, "start codex instead of claude")
	cmd.Flags().StringVar(&repo, "repo", "", "repository to branch from (default: the current directory's)")
	cmd.Flags().StringVar(&slug, "slug", "", "worktree and branch name (default: from the prompt)")
	return cmd
}

func printLaunch(w io.Writer, rec launch.Record, name, socket string) {
	id := "id pending"
	if rec.Session != "" {
		id = core.Short(rec.Session)
	}
	fmt.Fprintf(w, "%s %s (%s) in %s on branch %s\n", rec.Harness, rec.Key, id, rec.Worktree.Path, rec.Worktree.Branch)
	if rec.Parent != "" {
		fmt.Fprintf(w, "spawned by %s\n", core.Short(rec.Parent))
	}
	fmt.Fprintf(w, "open: ym open %s   (tmux -L %s attach -t %s)\n", rec.Key, socket, name)
}

// detach starts a background ym that links a Codex child to its parent once Codex has named its thread.
func detach(key string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(self, "launch-link", key)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	return c.Process.Release()
}

func linkLaunchCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:    "launch-link <key>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, err := newLauncher()
			if err != nil {
				return err
			}
			for deadline := time.Now().Add(3 * time.Minute); time.Now().Before(deadline); time.Sleep(time.Second) {
				rec, err := l.find(args[0])
				if err != nil {
					return err
				}
				if rec.Session != "" {
					return e.core.Store.Link(rec.Session, rec.Parent)
				}
			}
			return fmt.Errorf("%s: codex never recorded a thread", args[0])
		}),
	}
}

// state is how a launched session's pane stands: running, exited with a status, or gone with its tmux session.
func (l launcher) state(rec launch.Record) (tmux.Pane, string) {
	p, err := l.srv.Pane(tmux.SessionName(rec.Key))
	switch {
	case err != nil:
		return p, "gone"
	case p.Dead:
		return p, fmt.Sprintf("exited %d", p.Status)
	}
	return p, "running"
}

func launchedCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "launched",
		Short: "List sessions ym launched, and whether each agent still runs",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, err := newLauncher()
			if err != nil {
				return err
			}
			all, err := l.records.All()
			if err != nil {
				return err
			}
			for _, rec := range all {
				rec = l.settle(rec)
				_, st := l.state(rec)
				fmt.Fprintf(cmd.OutOrStdout(), "%-6s %-8s %-10s %-28s %s\n", rec.Harness, core.Short(rec.Session), st, rec.Key, rec.Worktree.Path)
			}
			return nil
		}),
	}
}

// revive resumes an exited launched session in its worktree, recreating the worktree when it is gone.
func (l launcher) revive(rec launch.Record) (string, error) {
	pane, st := l.state(rec)
	if st == "running" {
		return "", fmt.Errorf("%s is running; ym open %s", rec.Key, rec.Key)
	}
	if err := launch.Restore(rec.Worktree, rec.Head); err != nil {
		return "", err
	}
	if err := launch.Trust(rec.Harness, rec.Worktree.Path); err != nil {
		return "", err
	}
	argv, err := startArgv(rec)
	if rec.Session != "" {
		argv, err = resumeArgv(rec)
	}
	if err != nil {
		return "", err
	}
	name := tmux.SessionName(rec.Key)
	if pane.ID != "" {
		err = l.srv.Respawn(name, rec.Worktree.Path, argv)
	} else {
		_, err = l.srv.Start(l.conf, name, rec.Worktree.Path, argv)
	}
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("revived %s: %s", rec.Key, strings.Join(argv[1:], " ")), nil
}

// Dormant lists the launched sessions whose agents no longer run.
func (l launcher) Dormant() []core.Dormant {
	all, _ := l.records.All()
	var out []core.Dormant
	for _, rec := range all {
		if _, st := l.state(rec); st != "running" {
			out = append(out, core.Dormant{Key: rec.Key, Harness: rec.Harness, Session: rec.Session, Parent: rec.Parent, Cwd: rec.Worktree.Path,
				RepoKey: cleanDir(rec.Worktree.GitDir), Branch: rec.Worktree.Branch, Title: oneLine(rec.Prompt), State: st, Started: rec.Started})
		}
	}
	return out
}

func (l launcher) Revive(key string) (string, error) {
	rec, err := l.find(key)
	if err != nil {
		return "", err
	}
	return l.revive(rec)
}

func cleanDir(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func reviveCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "revive <key|session>",
		Short: "Resume an exited launched session in its worktree, recreating the worktree when it is gone",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, err := newLauncher()
			if err != nil {
				return err
			}
			rec, err := l.find(args[0])
			if err != nil {
				return err
			}
			msg, err := l.revive(rec)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		}),
	}
}

func landCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "land <key|session>",
		Short: "End a launched session and remove its worktree, only when its branch is clean and merged or pushed",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, err := newLauncher()
			if err != nil {
				return err
			}
			rec, err := l.find(args[0])
			if err != nil {
				return err
			}
			why := "worktree already gone"
			_, statErr := os.Stat(rec.Worktree.Path)
			if statErr == nil {
				g, err := launch.State(rec.Worktree.Path)
				if err != nil {
					return err
				}
				ok, reason := g.Landable()
				if !ok {
					return fmt.Errorf("not landing %s: %s %s", rec.Key, rec.Worktree.Path, reason)
				}
				why = reason
			}
			if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
				return err
			}
			if statErr == nil {
				if err := launch.Remove(rec.Worktree); err != nil {
					return err
				}
			}
			if err := l.records.Delete(rec.Key); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "landed %s (%s); branch %s kept\n", rec.Key, why, rec.Worktree.Branch)
			return nil
		}),
	}
}
