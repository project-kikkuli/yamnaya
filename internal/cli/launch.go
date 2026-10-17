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
	core    *core.Core
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

// codexDirs lets Codex's sandbox write ym's state, so ym item works, and what a commit in its worktree writes of the repository's git dir.
// A state dir set by $YAMNAYA_STATE_DIR is passed to Codex's shells, which drop most of the environment.
func codexDirs(rec launch.Record) []string {
	var args []string
	if path, err := core.DefaultStorePath(); err == nil {
		args = append(args, "--add-dir", filepath.Dir(path))
	}
	if dir := os.Getenv("YAMNAYA_STATE_DIR"); dir != "" {
		args = append(args, "-c", "shell_environment_policy.set.YAMNAYA_STATE_DIR="+strconv.Quote(dir))
	}
	for _, dir := range commitDirs(rec.Worktree) {
		args = append(args, "--add-dir", dir)
	}
	return args
}

// commitDirs are the parts of w's git dir a commit, branch or fetch writes, leaving its hooks and config out of the sandbox's reach.
func commitDirs(w launch.Worktree) []string {
	if w.GitDir == "" {
		return nil
	}
	dirs := []string{filepath.Join(w.GitDir, "objects"), filepath.Join(w.GitDir, "refs"), filepath.Join(w.GitDir, "logs")}
	if b, err := os.ReadFile(filepath.Join(w.Path, ".git")); err == nil {
		if admin, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: "); ok {
			dirs = append(dirs, admin)
		}
	}
	return dirs
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

// Start creates a worktree for the new session, then starts its agent there on ym's server.
func (l launcher) Start(n core.NewLaunch) (string, error) {
	slug := launch.NormalizeSlug(n.Slug)
	if slug == "" {
		slug = launch.Slug(n.Prompt)
	}
	rec := launch.Record{Harness: claude.Name, Prompt: n.Prompt, Parent: n.Parent, Started: time.Now()}
	switch n.Harness {
	case codex.Name:
		rec.Harness = codex.Name
	case "", claude.Name:
		rec.Session = uuid.NewString()
	default:
		return "", fmt.Errorf("unknown harness %q", n.Harness)
	}
	if _, err := bin(rec.Harness); err != nil {
		return "", err
	}
	var err error
	if rec.Worktree, err = launch.Create(n.Repo, slug); err != nil {
		return "", err
	}
	rec.Key, rec.Head = filepath.Base(rec.Worktree.Path), launch.Head(rec.Worktree.Path)
	argv, err := startArgv(rec)
	if err != nil {
		return "", err
	}
	if err := launch.Trust(rec.Harness, rec.Worktree.Path); err != nil {
		return "", err
	}
	if err := l.records.Save(rec); err != nil {
		return "", err
	}
	name := tmux.SessionName(rec.Key)
	if _, err := l.srv.Start(l.conf, name, rec.Worktree.Path, argv); err != nil {
		return "", err
	}
	if rec.Parent != "" {
		if rec.Session != "" {
			if err := l.core.Store.Link(rec.Session, rec.Parent); err != nil {
				return "", err
			}
		} else if err := detach("launch-link", rec.Key); err != nil {
			return "", err
		}
	}
	var b strings.Builder
	printLaunch(&b, rec, name, l.srv.Socket)
	return strings.TrimRight(b.String(), "\n"), nil
}

func newCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var useCodex bool
	var repo, slug string
	cmd := &cobra.Command{
		Use:   "new [--codex] [--repo NAME|PATH] [--slug S] <prompt>",
		Short: "Start claude (or codex) on a prompt in a new worktree, in ym's own tmux server",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if e.core.Launches == nil {
				return errors.New("ym's launcher is unavailable")
			}
			n := core.NewLaunch{Prompt: args[0], Harness: claude.Name, Repo: repo, Slug: slug}
			if useCodex {
				n.Harness = codex.Name
			}
			var err error
			if n.Repo == "" {
				n.Repo, err = os.Getwd()
			} else {
				n.Repo, err = e.core.ResolveRepo(n.Repo)
			}
			if err != nil {
				return err
			}
			if c, err := e.caller(); err == nil {
				n.Parent = c.Session
			} else if !errors.Is(err, core.ErrNotFound) {
				return err
			}
			msg, err := e.core.StartNew(n)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		}),
	}
	cmd.Flags().BoolVar(&useCodex, "codex", false, "start codex instead of claude")
	cmd.Flags().StringVar(&repo, "repo", "", "repository to branch from, by path or by any part of its name (default: the current directory's)")
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

// detach runs ym with args in the background, outliving this ym.
func detach(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(self, args...)
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
	case p.Dead && p.Signal != "":
		return p, "sig" + p.Signal
	case p.Dead:
		return p, fmt.Sprintf("exited %d", p.Status)
	}
	return p, "running"
}

func (l launcher) Launched() []core.Launched {
	all, _ := l.records.All()
	out := make([]core.Launched, 0, len(all))
	for _, rec := range all {
		rec = l.settle(rec)
		_, st := l.state(rec)
		out = append(out, core.Launched{Key: rec.Key, Harness: rec.Harness, Session: rec.Session, State: st, Path: rec.Worktree.Path})
	}
	return out
}

func launchedCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:   "launched",
		Short: "List sessions ym launched, and whether each agent still runs",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if e.core.Launches == nil {
				return errors.New("ym's launcher is unavailable")
			}
			for _, l := range e.core.Launches.Launched() {
				fmt.Fprintln(cmd.OutOrStdout(), l)
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
	if rec.Harness == codex.Name && rec.Session != "" {
		if err := detach("resume-watch", name); err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("revived %s: %s", rec.Key, strings.Join(argv[1:], " ")), nil
}

// Dormant lists the launched sessions whose agents no longer run, landing first those whose agent exited 0 and lost nothing.
func (l launcher) Dormant() []core.Dormant {
	all, _ := l.records.All()
	var out []core.Dormant
	for _, rec := range all {
		pane, st := l.state(rec)
		if pane.Dead && pane.Signal == "" && pane.Status == 0 {
			_, err := l.land(rec)
			if err == nil {
				continue
			}
			if !errors.As(err, &unlandable{}) {
				st = "land failed: " + err.Error()
			}
		}
		if st != "running" {
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

// unlandable is land's refusal while removing the worktree would lose work.
type unlandable struct{ key, path, reason string }

func (u unlandable) Error() string {
	return fmt.Sprintf("not landing %s: %s %s", u.key, u.path, u.reason)
}

// Land ends a launched session and removes its worktree, refusing while its branch has work that is not clean and merged or pushed.
func (l launcher) Land(ref string) (string, error) {
	rec, err := l.find(ref)
	if err != nil {
		return "", err
	}
	return l.land(rec)
}

// Forget ends a launched session and drops its record, leaving its worktree and branch as they are.
func (l launcher) Forget(ref string) (string, error) {
	rec, err := l.find(ref)
	if err != nil {
		return "", err
	}
	if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
		return "", err
	}
	if err := l.records.Delete(rec.Key); err != nil {
		return "", err
	}
	return fmt.Sprintf("forgot %s; %s and branch %s stay", rec.Key, rec.Worktree.Path, rec.Worktree.Branch), nil
}

func (l launcher) land(rec launch.Record) (string, error) {
	if rec.Adopted {
		if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
			return "", err
		}
		if err := l.records.Delete(rec.Key); err != nil {
			return "", err
		}
		return fmt.Sprintf("landed %s; %s was not ym's, so it stays", rec.Key, rec.Worktree.Path), nil
	}
	why := "worktree already gone"
	_, statErr := os.Stat(rec.Worktree.Path)
	if statErr == nil {
		g, err := launch.State(rec.Worktree.Path)
		if err != nil {
			return "", err
		}
		ok, reason := g.Landable()
		if !ok {
			return "", unlandable{rec.Key, rec.Worktree.Path, reason}
		}
		why = reason
	}
	if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
		return "", err
	}
	if statErr == nil {
		if err := launch.Remove(rec.Worktree); err != nil {
			return "", err
		}
	}
	if err := l.records.Delete(rec.Key); err != nil {
		return "", err
	}
	return fmt.Sprintf("landed %s (%s); branch %s kept", rec.Key, why, rec.Worktree.Branch), nil
}

func landCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "land <key|session>",
		Short: "End a launched session and remove its worktree, only when its branch is clean and merged or pushed; an adopted session's directory stays",
		Args:  cobra.ExactArgs(1),
	}
	forget := cmd.Flags().Bool("forget", false, "end the session and drop its launch record, never touching its worktree or branch")
	cmd.RunE = with(func(e *env, cmd *cobra.Command, args []string) error {
		if e.core.Launches == nil {
			return errors.New("ym's launcher is unavailable")
		}
		land := e.core.Launches.Land
		if *forget {
			land = e.core.Launches.Forget
		}
		msg, err := land(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), msg)
		return nil
	})
	return cmd
}

// resumeWatchCmd answers Codex's "open in another app" notice with its own retry key. Codex's shared app-server keeps a quit
// client's thread loaded for a minute, and a resume whose -c overrides differ from that load is refused until it unloads.
func resumeWatchCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "resume-watch <tmux-session>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			srv := tmux.NewServer()
			end, seen := time.Now().Add(15*time.Second), false
			for ; time.Now().Before(end); time.Sleep(500 * time.Millisecond) {
				p, err := srv.Pane(args[0])
				if err != nil || p.Dead {
					return nil
				}
				screen, err := srv.Screen(core.Handle{ID: p.ID})
				if err != nil || !codex.OpenElsewhere(screen) {
					continue
				}
				if !seen {
					end, seen = time.Now().Add(90*time.Second), true
				}
				if err := srv.Keys(p.ID, "r"); err != nil {
					return err
				}
				time.Sleep(2 * time.Second)
			}
			return nil
		},
	}
}
