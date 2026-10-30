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
	"sync"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"github.com/project-kikkuli/yamnaya/internal/tui"
)

type launcher struct {
	rootOf     func(int, string) string
	resumeNote string
	operation  string
	core       *core.Core
	srv        *tmux.Host
	records    launch.Records
	conf       string
	// stamp is the file whose age throttles the board's background gc; empty never starts one.
	stamp  string
	squash *launch.Squash
	// repos caches the repositories with linked worktrees found under ~/dev.
	repos string
	// landings is the JSON-lines log every landing appends to; empty logs nothing.
	landings string
	// removals is the log gc appends a line to for every worktree, branch and record it removes; empty logs nothing.
	removals string
	// everywhere lets gc sweep repositories its launch records did not make worktrees in; only the default state dir does.
	everywhere bool
}

// newLauncher is the launcher keeping its records, tmux config and logs in the state directory dir.
func newLauncher(dir string) (launcher, error) {
	l := launcher{srv: tmux.NewServer(), records: launch.Records{Dir: filepath.Join(dir, "launches")}, conf: filepath.Join(dir, "tmux.conf"),
		stamp: filepath.Join(dir, "gc.stamp"), repos: filepath.Join(dir, "repos.json"), landings: filepath.Join(dir, "landings.jsonl"),
		removals: filepath.Join(dir, "gc.log"), squash: &launch.Squash{Hub: launch.GH{}, Cache: filepath.Join(dir, "squash.json"), Now: time.Now}}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return l, err
	}
	l.everywhere = core.DefaultStateDir(dir)
	conf := tmux.Conf()
	if old, err := os.ReadFile(l.conf); err == nil && string(old) == conf {
		return l, nil
	}
	if err := os.WriteFile(l.conf, []byte(conf), 0o644); err != nil {
		return l, err
	}
	return l, l.srv.Reload(l.conf)
}

// bin is the absolute path of a harness binary, so the ym server needs no PATH of its own.
func bin(harnessName string) (string, error) {
	name := map[string]string{claude.Name: claude.New().Bin, codex.Name: codex.New().Bin}[harnessName]
	path, err := exec.LookPath(name)
	if err != nil {
		return "", fmt.Errorf("%s not found on PATH: %w", name, err)
	}
	if harnessName == codex.Name {
		return codex.Native(path), nil
	}
	return path, nil
}

func startArgv(rec launch.Record) ([]string, error) {
	if rec.ConfigRoot != "" {
		if err := recordProfile(rec).ValidateSubscription(rec.Worktree.Path); err != nil {
			return nil, err
		}
	}
	b, err := bin(rec.Harness)
	if err != nil {
		return nil, err
	}
	prompt := rec.Prompt + slotNote
	model := envModel(rec.Harness)
	if rec.Harness == claude.Name {
		argv := []string{b, "--dangerously-skip-permissions", "--session-id", rec.Session}
		if model != "" {
			argv = append(argv, "--model", model)
		}
		if rec.Access != "" {
			argv = append(append(argv, accessArgs(rec)...), "--")
		}
		return scopedArgv(rec, append(argv, prompt)), nil
	}
	argv := []string{b, "--yolo"}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	return scopedArgv(rec, append(append(argv, codexDirs(rec)...), prompt)), nil
}

// envModel is the model the environment pins every session of harnessName to; rehearsals set it so their sessions run on a cheap model.
func envModel(harnessName string) string {
	return os.Getenv(map[string]string{claude.Name: "YAMNAYA_CLAUDE_MODEL", codex.Name: "YAMNAYA_CODEX_MODEL"}[harnessName])
}

// slotNote ends every prompt ym starts an agent on, so heavy work across all agents shares the machine's slots.
const slotNote = "\n\nThis machine is shared by many agents. Run every heavy command (test suites, builds, linters over a whole repo, long jobs) as " +
	"`ym slot -- <command> [args...]`: it waits for one of the machine's shared slots and runs the command niced. Quick commands need no slot. " +
	"`ym slot --status` shows who holds and waits for the slots."

// resumeArgv continues rec's conversation, with prompt as its next message unless it is empty.
func resumeArgv(rec launch.Record, prompt string) ([]string, error) {
	if rec.ConfigRoot != "" {
		rec.Account = recordProfile(rec).Account
	}
	if rec.Handoff != "" {
		if prompt != "" && prompt != rec.Handoff {
			prompt = rec.Handoff + "\n\n" + prompt
		} else {
			prompt = rec.Handoff
		}
	}
	if rec.ConfigRoot != "" {
		if err := recordProfile(rec).ValidateSubscription(rec.Worktree.Path); err != nil {
			return nil, err
		}
	}
	b, err := bin(rec.Harness)
	if err != nil {
		return nil, err
	}
	var argv []string
	if rec.Harness == claude.Name {
		argv = []string{b, "--dangerously-skip-permissions", "--resume", rec.Session}
	} else {
		argv = append(append([]string{b, "resume", "--yolo"}, codexDirs(rec)...), rec.Session)
	}
	model := rec.Model
	if model == "" {
		model = envModel(rec.Harness)
	}
	if model != "" {
		argv = append(argv, "--model", model)
	}
	if rec.Harness == claude.Name && rec.Access != "" {
		argv = append(append(argv, accessArgs(rec)...), "--")
	}
	if prompt != "" {
		argv = append(argv, prompt)
	}
	return scopedArgv(rec, argv), nil
}

// transcript is the path of rec's conversation, empty when its harness keeps none.
func transcript(rec launch.Record) string {
	if rec.Session == "" {
		return ""
	}
	if rec.Harness == codex.Name {
		return recordCodex(rec).Transcript(rec.Session)
	}
	return recordClaude(rec).Transcript(rec.Session)
}

// codexDirs lets Codex's sandbox write ym's state, so ym item works, and what a commit in its worktree writes of the repository's git dir.
// The state dir, tmux server and serve choice set in ym's environment are passed to Codex's shells, which drop most of the environment.
func codexDirs(rec launch.Record) []string {
	args := accessArgs(rec)
	if rec.ConfigRoot != "" {
		args = append(args, "--no-daemon")
	}
	if rec.Profile != "" && rec.Profile != "default" {
		args = append(args, "-c", "sqlite_home="+strconv.Quote(rec.ConfigRoot))
	}
	if path, err := core.DefaultStorePath(); err == nil {
		args = append(args, "--add-dir", filepath.Dir(path))
	}
	for _, key := range []string{"YAMNAYA_STATE_DIR", "YAMNAYA_YM_SOCKET", "YAMNAYA_NO_SERVE", "CLAUDE_CONFIG_DIR", "CLAUDE_SECURESTORAGE_CONFIG_DIR"} {
		if v := os.Getenv(key); v != "" {
			args = append(args, "-c", "shell_environment_policy.set."+key+"="+strconv.Quote(v))
		}
	}
	if rec.ConfigRoot != "" {
		args = append(args, "-c", "shell_environment_policy.set.CODEX_HOME="+strconv.Quote(rec.ConfigRoot), "-c", "shell_environment_policy.set.YAMNAYA_PROFILE="+strconv.Quote(profile.ID(rec.Profile)), "-c", "shell_environment_policy.set.YAMNAYA_ACCOUNT="+strconv.Quote(rec.Account))
	}
	if rec.ConfigRoot != "" {
		args = append(args, "-c", "shell_environment_policy.set.CODEX_SQLITE_HOME="+strconv.Quote(sqliteRoot(recordProfile(rec))))
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
	if rec.Handoff != "" && l.core != nil {
		if s, err := l.core.Find(rec.Harness + ":" + profile.ID(rec.Profile) + "@" + rec.Session); err == nil && strings.Contains(s.Ask, "The prior turn stopped because") {
			rec.Handoff = ""
			l.records.Save(rec)
		}
	}
	if rec.Session != "" || rec.Harness != codex.Name {
		return rec
	}
	if id := recordCodex(rec).LatestIn(rec.Worktree.Path, rec.Started); id != "" {
		rec.Session = id
		l.records.Save(rec)
	}
	return rec
}

func (l launcher) find(ref string) (launch.Record, error) {
	rec, err := l.lookup(ref)
	if err != nil {
		return rec, fmt.Errorf("no unique session launched by ym matches %q; see ym launched: %w", ref, err)
	}
	if head := launch.Head(rec.Worktree.Path); head != "" && head != rec.Head {
		rec.Head = head
		l.records.Save(rec)
	}
	return l.settle(rec), nil
}

func (l launcher) lookup(ref string) (launch.Record, error) {
	rec, err := l.records.Lookup(ref)
	if err != nil || l.core == nil || rec.Key == ref || rec.Worktree.Branch == ref || rec.Session == ref {
		return rec, err
	}
	live, err := l.core.Find(ref)
	if errors.Is(err, core.ErrNotFound) {
		return rec, nil
	}
	if err != nil {
		return launch.Record{}, err
	}
	if live.ID != rec.Session {
		return launch.Record{}, fmt.Errorf("%w %q: %s and %s", core.ErrAmbiguous, ref, rec.Session, live.ID)
	}
	return rec, nil
}

// attach shows a launched session in this terminal, then returns to the board.
func (e *env) attach(cmd *exec.Cmd) error {
	if err := cmd.Run(); err != nil {
		return err
	}
	return tui.Run(e.core)
}

// openLaunched opens a session by its launch key, showing it with show; ok is false when ref names no launch.
func (e *env) openLaunched(w io.Writer, ref string, show func(*exec.Cmd) error) (bool, error) {
	l, err := newLauncher(e.core.StateDir)
	if err != nil {
		return false, err
	}
	rec, err := l.lookup(ref)
	if err != nil {
		if errors.Is(err, core.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	if !rec.Seen.IsZero() {
		if _, err := e.core.Find(rec.Session); err == nil {
			return false, nil
		}
	}
	pane, st := l.state(rec)
	switch {
	case rec.IsLanded():
		st = "landed " + rec.Landed.Format(time.DateTime)
	case st == "running":
	case codeGone(rec) != "":
		st = codeGone(rec)
	case !rec.Seen.IsZero():
		st = "ended outside ym"
	}
	if st != "running" {
		fmt.Fprintf(w, "%s %s; ym revive %s\n", rec.Key, st, rec.Key)
		return true, nil
	}
	t := core.Target{Session: core.Session{Harness: rec.Harness, ID: rec.Session}, Handle: &core.Handle{Host: l.srv.Name(), ID: pane.ID, Label: pane.Session}}
	c, err := e.core.AttachCmd(t)
	if err != nil {
		return true, err
	}
	if c != nil {
		return true, show(c)
	}
	return true, e.core.Focus(t)
}

// boilerplate is text a new session's prompt may open with that says nothing of its task: the instruction files its
// agent reads anyway and the earlier prompts of the session launching it.
func (l launcher) boilerplate(n core.NewLaunch) []string {
	out := launch.Instructions(core.MainCheckout(n.Repo))
	if n.Parent == "" {
		return out
	}
	recs, _ := l.records.All()
	for _, r := range recs {
		if r.Parent == n.Parent {
			out = append(out, r.Prompt)
		}
	}
	return out
}

// Start creates a worktree for the new session, then starts its agent there on ym's server.
func (l launcher) Start(n core.NewLaunch) (core.Started, error) {
	slug := launch.NormalizeSlug(n.Slug)
	if slug == "" {
		slug = launch.Unique(launch.Slug(launch.Strip(n.Prompt, l.boilerplate(n))))
	}
	rec := launch.Record{Harness: claude.Name, Prompt: n.Prompt, Parent: n.Parent, Started: time.Now()}
	switch n.Harness {
	case codex.Name:
		rec.Harness = codex.Name
	case "", claude.Name:
		rec.Session = n.Session
		if rec.Session == "" {
			rec.Session = uuid.NewString()
		}
	default:
		return core.Started{}, fmt.Errorf("unknown harness %q", n.Harness)
	}
	if _, err := bin(rec.Harness); err != nil {
		return core.Started{}, err
	}
	preferred := ""
	if n.Profile == "" && n.Parent != "" {
		if parent, ok := l.records.Find(n.Parent); ok && parent.Harness == rec.Harness {
			preferred = parent.Profile
		} else if l.core != nil {
			if parent, err := l.core.Find(n.Parent); err == nil && parent.Harness == rec.Harness {
				preferred = parent.Profile
			}
		}
	}
	selected, err := l.selectProfile(rec.Harness, n.Profile)
	if n.Profile == "" && preferred != "" {
		if candidate, e := l.selectProfile(rec.Harness, preferred); e == nil && profileUsable(candidate, profileLimits(filepath.Dir(l.records.Dir)), time.Now()) {
			selected, err = candidate, nil
		}
	}
	if err != nil {
		return core.Started{}, err
	}
	if n.ConfigRoot != "" && n.ConfigRoot != selected.Root {
		return core.Started{}, fmt.Errorf("profile root changed for %s", selected.ID)
	}
	setRecordProfile(&rec, selected)
	if err = provisionProfile(selected); err != nil {
		return core.Started{}, err
	}
	if n.Directory != "" {
		if !filepath.IsAbs(n.Directory) {
			return core.Started{}, fmt.Errorf("coordination workspace must be absolute")
		}
		if err := os.MkdirAll(n.Directory, 0o755); err != nil {
			return core.Started{}, err
		}
		rec.Worktree = launch.Worktree{Path: n.Directory}
		rec.Key = slug
		for i := 2; l.records.Has(rec.Key); i++ {
			rec.Key = slug + "-" + strconv.Itoa(i)
		}
		if n.Repo != "" {
			rec.Access = n.Repo
		}
	} else {
		if rec.Worktree, err = launch.Create(n.Repo, slug, func(p string) bool { return l.records.Has(filepath.Base(p)) }); err != nil {
			return core.Started{}, err
		}
		rec.Key, rec.Head, rec.Repo = filepath.Base(rec.Worktree.Path), launch.Head(rec.Worktree.Path), core.RepoID(rec.Worktree.GitDir)
	}
	argv, err := startArgv(rec)
	if err != nil {
		return core.Started{}, err
	}
	if err := trustRecord(rec); err != nil {
		return core.Started{}, err
	}
	rec.Starting = true
	if err := l.records.Save(rec); err != nil {
		return core.Started{}, err
	}
	name := tmux.SessionName(rec.Key)
	pane, startErr := l.srv.Start(l.conf, name, rec.Worktree.Path, argv)
	if err := startErr; err != nil {
		return core.Started{}, err
	}
	rec.AttachedAt = time.Now()
	rec.NativePID = pane.PID
	rec.Dedicated = rec.Harness == codex.Name
	rec.Starting = false
	if err := l.records.Save(rec); err != nil {
		return core.Started{}, err
	}
	if n.Await {
		if rec, err = l.await(rec); err != nil {
			return core.Started{}, err
		}
	}
	if rec.Parent != "" {
		if rec.Session != "" {
			if err := l.core.Store.Link(rec.Session, rec.Parent); err != nil {
				return core.Started{}, err
			}
		} else if err := detach("launch-link", rec.Key); err != nil {
			return core.Started{}, err
		}
	}
	var b strings.Builder
	printLaunch(&b, rec, name, l.srv.Socket)
	return core.Started{Key: rec.Key, Session: rec.Session, Profile: rec.Profile, ConfigRoot: rec.ConfigRoot, Message: strings.TrimRight(b.String(), "\n")}, nil
}

// await waits until Codex records rec's thread; a session that records none in time is ended, so no agent runs that ym cannot name.
func (l launcher) await(rec launch.Record) (launch.Record, error) {
	for deadline := time.Now().Add(core.StartGrace); rec.Session == ""; time.Sleep(250 * time.Millisecond) {
		if time.Now().After(deadline) {
			l.forget(rec)
			return rec, fmt.Errorf("codex in %s recorded no thread within %s, so ym ended it", rec.Key, core.StartGrace)
		}
		rec = l.settle(rec)
	}
	return rec, nil
}

func newCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var useCodex bool
	var repo, slug, selected, source string
	cmd := &cobra.Command{
		Use:   "new [--codex] [--repo NAME|PATH] [--slug S] <prompt>",
		Short: "Start claude (or codex) on a prompt in a new worktree, in ym's own tmux server",
		Args:  cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if e.core.Launches == nil {
				return errors.New("ym's launcher is unavailable")
			}
			if err := ensureWorkService(); err != nil {
				return err
			}
			n := core.NewLaunch{Prompt: args[0], Harness: claude.Name, Repo: repo, Slug: slug, Profile: selected, Source: source}
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
	cmd.Flags().StringVar(&selected, "profile", "", "login profile id on this host")
	cmd.Flags().BoolVar(&useCodex, "codex", false, "start codex instead of claude")
	cmd.Flags().StringVar(&repo, "repo", "", "repository to branch from, by path or by any part of its name (default: the current directory's)")
	cmd.Flags().StringVar(&source, "source", "", "stable work identity for idempotent launch replay")
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
var detach = detachSelf

func detachSelf(args ...string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	c := exec.Command(self, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := c.Start(); err != nil {
		return err
	}
	reap(c.Process.Pid, func() { c.Wait() })
	return nil
}

func linkLaunchCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{
		Use:    "launch-link <key>",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, err := newLauncher(e.core.StateDir)
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
	if errors.Is(err, core.ErrNotFound) {
		return p, missing(rec, time.Now())
	}
	if err != nil {
		return p, core.LaunchUnknown
	}
	return p, paneState(p)
}

// missing is the state of a record whose tmux session is not there: gone, or unknown while the launch that saved it before
// starting its session may still be starting it.
func missing(rec launch.Record, now time.Time) string {
	if rec.Starting && now.Sub(rec.Started) < core.StartGrace {
		return core.LaunchUnknown
	}
	return "gone"
}

// stateIn is a record's state read from a pane table taken once for many records, unknown when tmux could not list it.
func stateIn(panes map[string]tmux.Pane, listErr error, rec launch.Record) string {
	if listErr != nil {
		return core.LaunchUnknown
	}
	p, ok := panes[tmux.SessionName(rec.Key)]
	if !ok {
		return missing(rec, time.Now())
	}
	return paneState(p)
}

func paneState(p tmux.Pane) string {
	switch {
	case p.Dead && p.Signal != "":
		return "sig" + p.Signal
	case p.Dead:
		return fmt.Sprintf("exited %d", p.Status)
	}
	return "running"
}

// worktrees are the directories of every launch this launcher keeps a record of.
func (l launcher) worktrees() []string {
	all, _ := l.records.All()
	out := make([]string, 0, len(all))
	for _, rec := range all {
		out = append(out, rec.Worktree.Path)
	}
	return out
}

func (l launcher) Launched() []core.Launched {
	all, _ := l.records.All()
	panes, listErr := l.srv.Panes()
	out := make([]core.Launched, 0, len(all))
	for _, rec := range all {
		if rec.IsLanded() {
			out = append(out, core.Launched{Key: rec.Key, Harness: rec.Harness, Profile: rec.Profile, Session: rec.Session, State: "landed", Path: rec.Worktree.Path, Landed: rec.Landed,
				Killed: rec.Killed, Recovered: rec.Recovery.Bundle != ""})
			continue
		}
		if !rec.Seen.IsZero() {
			continue
		}
		rec = l.settle(rec)
		st := stateIn(panes, listErr, rec)
		if st == "gone" {
			// A table read before a launch's session started lacks it, so gone goes by the pane as it is now.
			_, st = l.state(rec)
		}
		if was := rec; mark(&rec, st) != was {
			l.records.Save(rec)
		}
		out = append(out, core.Launched{Key: rec.Key, Harness: rec.Harness, Profile: rec.Profile, Session: rec.Session, State: st, Path: rec.Worktree.Path,
			Parent: rec.Parent, Killed: rec.Killed, Revive: rec.Revive})
	}
	return out
}

// mark records whether rec's agent runs, and, when its pane went under an agent last seen running without ym kill, as a restart
// of the host or of ym's tmux server leaves it, that it resumes.
func mark(rec *launch.Record, state string) launch.Record {
	switch {
	case state == "running":
		rec.Running, rec.Revive = true, false
	case state == "gone" && rec.Running && rec.Killed.IsZero():
		rec.Running, rec.Revive = false, true
	case state != core.LaunchUnknown && state != "gone":
		rec.Running = false
	}
	return *rec
}

func launchedCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var landed bool
	cmd := &cobra.Command{
		Use:   "launched [--landed]",
		Short: "List sessions ym launched, and whether each agent still runs",
		Args:  cobra.NoArgs,
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			if e.core.Launches == nil {
				return errors.New("ym's launcher is unavailable")
			}
			for _, l := range e.core.Launches.Launched() {
				if landed || l.Landed.IsZero() {
					fmt.Fprintln(cmd.OutOrStdout(), l)
				}
			}
			return nil
		}),
	}
	cmd.Flags().BoolVar(&landed, "landed", false, "also list landed sessions, and those ym saw running elsewhere a day after they ended, which ym revive brings back")
	return cmd
}

// revive resumes an exited launched session in its worktree, recreating the worktree when it is gone.
func (l launcher) revive(rec launch.Record) (string, error) {
	_, lost, err := l.restart(rec, func(rec *launch.Record) ([]string, error) {
		if rec.Session != "" {
			return resumeArgv(*rec, l.resumeNote)
		}
		return startArgv(*rec)
	})
	if err != nil {
		return "", err
	}
	if lost != "" {
		return fmt.Sprintf("revived %s %s (%s) in %s; open: ym open %s", rec.Harness, rec.Key, lost, rec.Worktree.Path, rec.Key), nil
	}
	return fmt.Sprintf("revived %s %s in %s; open: ym open %s", rec.Harness, rec.Key, rec.Worktree.Path, rec.Key), nil
}

// Resume continues session, a session ym launched, in its own conversation with prompt as its next message or, when its transcript
// is gone, starts it over in its worktree on start, or on its first prompt when start is empty.
func (l launcher) Resume(session, prompt, start string) (string, bool, error) {
	rec, ok := l.records.Find(session)
	if !ok || rec.Session != session {
		return "", false, fmt.Errorf("no session launched by ym is %s: %w", session, core.ErrNotFound)
	}
	resumed := transcript(rec) != ""
	if !resumed && rec.Profile != "" && rec.Profile != "default" {
		return "", false, fmt.Errorf("profile %s conversation is unavailable; refusing to replace it with a new session", rec.Profile)
	}
	_, _, err := l.restart(rec, func(rec *launch.Record) ([]string, error) {
		if resumed {
			return resumeArgv(*rec, prompt)
		}
		if rec.Harness == claude.Name {
			if err := recordClaude(*rec).Unstart(rec.Session); err != nil {
				return nil, err
			}
		}
		fresh := *rec
		if start != "" {
			fresh.Prompt = start
		}
		if rec.Harness == codex.Name {
			rec.Session, rec.Started = "", time.Now()
		}
		return startArgv(fresh)
	})
	if err != nil || resumed || rec.Harness != codex.Name {
		return session, resumed, err
	}
	rec, _ = l.records.Find(rec.Key)
	rec, err = l.await(rec)
	return rec.Session, resumed, err
}

// Bring resumes s, a session that ran outside ym's server, on ym's server in its own directory and conversation, with prompt as
// its next message; it fails when its transcript is gone.
func (l launcher) Bring(s core.Live, prompt string) error {
	rec, err := l.takeOver(s, s.Harness, s.ID)
	if err != nil {
		return err
	}
	if transcript(rec) == "" {
		return fmt.Errorf("its conversation's transcript is gone")
	}
	_, _, err = l.restart(rec, func(rec *launch.Record) ([]string, error) { return resumeArgv(*rec, prompt) })
	return err
}

// restart runs the command argv makes for rec in rec's pane, recreating its worktree when it is gone, and keeps rec unlanded.
func (l launcher) restart(rec launch.Record, argv func(*launch.Record) ([]string, error)) ([]string, string, error) {
	if err := recordProfile(rec).DefaultConstraint(); err != nil {
		return nil, "", err
	}
	if rec.Session != "" && l.operation != rec.Session {
		release, err := profile.Operation(filepath.Dir(l.records.Dir), rec.Harness, rec.Session)
		if err != nil {
			return nil, "", err
		}
		defer release()
		l.operation = rec.Session
		var errRecover error
		rec, errRecover = l.recoverMigration(rec)
		if errRecover != nil {
			return nil, "", errRecover
		}
	}

	pane, st := l.state(rec)
	if st == "running" {
		return nil, "", fmt.Errorf("%s is running; ym open %s", rec.Key, rec.Key)
	}
	if st == core.LaunchUnknown {
		return nil, "", fmt.Errorf("tmux could not say whether %s still runs; try again", rec.Key)
	}
	if !rec.Seen.IsZero() && l.core != nil {
		if s, err := l.core.Find(rec.Session); err == nil && s.Remote == "" {
			return nil, "", fmt.Errorf("%s still runs outside ym in %s; ym open %s", rec.Key, s.Location(), core.Short(rec.Session))
		}
	}
	was := rec
	clones := l.clones(rec)
	if rec.Recovery != (launch.Recovery{}) {
		for _, gd := range append([]string{rec.Worktree.GitDir}, clones...) {
			if _, err := os.Stat(gd); err == nil && gd != "" {
				if err := rec.Recovery.Fetch(gd, rec.Key); err != nil {
					return nil, "", err
				}
				break
			}
		}
	}
	back, lost, err := launch.Restore(rec.Worktree, rec.Head, clones)
	if err != nil {
		return nil, "", err
	}
	rec.Worktree = back
	if rec.Recovery != (launch.Recovery{}) && lost == "" {
		if err := rec.Recovery.Apply(rec.Worktree.Path, rec.Key); err != nil {
			return nil, "", fmt.Errorf("restore the work ym kill kept of %s: %w", rec.Key, err)
		}
		rec.Recovery = launch.Recovery{}
	} else if rec.Recovery.Bundle != "" && lost == launch.RepoGone {
		if err := rec.Recovery.Rebuild(rec.Worktree.Path, rec.Key, rec.Head, rec.Worktree.Branch); err != nil {
			lost = err.Error()
		} else {
			rec.Worktree.GitDir, rec.Recovery, lost = filepath.Join(rec.Worktree.Path, ".git"), launch.Recovery{}, ""
		}
	}
	var alignErr error
	rec, alignErr = l.chiefRecord(rec)
	if alignErr != nil {
		return nil, "", alignErr
	}
	if err := trustRecord(rec); err != nil {
		return nil, "", err
	}
	if err := provisionProfile(recordProfile(rec)); err != nil {
		return nil, "", err
	}
	rec.Account = recordProfile(rec).Account
	rec.AttachedAt = time.Now()
	args, err := argv(&rec)
	if err != nil {
		return nil, "", err
	}
	name := tmux.SessionName(rec.Key)
	if pane.ID != "" {
		err = l.srv.Respawn(name, rec.Worktree.Path, args)
	} else {
		_, err = l.srv.Start(l.conf, name, rec.Worktree.Path, args)
	}
	if err != nil {
		return nil, "", err
	}
	if p, err := l.srv.Pane(name); err == nil {
		rec.NativePID = p.PID
		rec.Dedicated = rec.Harness == codex.Name
	}
	rec.Landed, rec.Killed, rec.Revive, rec.Seen, rec.Goal, rec.Blocked, rec.Stamp = time.Time{}, time.Time{}, false, time.Time{}, "", "", ""
	if rec != was {
		if err := l.records.Save(rec); err != nil {
			return nil, "", err
		}
	}
	if rec.Harness == codex.Name && rec.Session != "" {
		if err := detach("resume-watch", name); err != nil {
			return nil, "", err
		}
	}
	if l.core != nil && l.core.Store != nil && rec.Session != "" {
		if err = l.core.Store.ClearKill(rec.Session); err != nil {
			return nil, "", err
		}
	}
	return args, lost, nil
}

// landsAlone reports whether a dormant session is cleaned up by itself: at once on any exit, a death by a signal, a gone pane
// or after ym kill. A session whose pane went while its agent ran, as a restart of the host leaves it, or a goal's agent whose
// pane went without ym kill, is resumed instead; one ym only saw running elsewhere stays revivable from the board for seenKeep.
func landsAlone(rec launch.Record, state string, now time.Time, work func(session, parent string) bool) bool {
	switch {
	case state == "running", state == core.LaunchUnknown:
		return false
	case !rec.Seen.IsZero():
		return now.Sub(rec.Seen) >= seenKeep
	case !rec.Killed.IsZero():
		return true
	case state == "gone" && (rec.Revive || rec.Running || work(rec.Session, rec.Parent)):
		return false
	}
	return true
}

// Dormant lists the launched sessions whose agents no longer run. It reads records and tmux only; landing is Sweep's, in ym serve.
func (l launcher) Dormant() []core.Dormant {
	all, _ := l.records.All()
	panes, listErr := l.srv.Panes()
	var out []core.Dormant
	for _, rec := range all {
		if rec.IsLanded() {
			continue
		}
		st := stateIn(panes, listErr, rec)
		if lost := codeGone(rec); lost != "" && st == "gone" && rec.Killed.IsZero() {
			st = lost
		} else if st == "gone" && !rec.Seen.IsZero() {
			st = "ended outside ym"
		}
		if st != "running" && st != core.LaunchUnknown {
			out = append(out, core.Dormant{Key: rec.Key, Harness: rec.Harness, Profile: rec.Profile, ConfigRoot: rec.ConfigRoot, Session: rec.Session, Parent: rec.Parent, Cwd: rec.Worktree.Path,
				RepoKey: cleanDir(rec.Worktree.GitDir), Branch: rec.Worktree.Branch, Title: oneLine(rec.Prompt), State: st, Started: rec.Started})
		}
	}
	return out
}

// Sweep cleans up every dormant launch the board saw stopped, in seen by key with its state, that lands alone: one that loses
// nothing lands, the rest end as a kill does, their work bundled first, so ym revive brings them back. A cleanup that refuses,
// as when git fails, is recorded and not tried again until the worktree changes; one a process works in waits for it to leave.
func (l launcher) Sweep(seen map[string]string) error {
	l.sweepSoon()
	all, err := l.records.All()
	if err != nil {
		return err
	}
	dirs := sync.OnceValue(cwds)
	work := sync.OnceValue(l.goalWork)
	keep := func(session, parent string) bool { return work()(session, parent) }
	var errs []error
	for _, rec := range all {
		if rec.Repo == "" && rec.Worktree.GitDir != "" {
			if _, err := os.Stat(rec.Worktree.GitDir); err == nil {
				rec.Repo = core.RepoID(rec.Worktree.GitDir)
				l.records.Save(rec)
			}
		}
		was, ok := seen[rec.Key]
		if !rec.Seen.IsZero() {
			was, ok = "gone", true
		}
		if rec.IsLanded() || !ok || !landsAlone(rec, was, time.Now(), keep) {
			continue
		}
		_, st := l.state(rec)
		if !landsAlone(rec, st, time.Now(), keep) {
			continue
		}
		if rec.Blocked != "" && rec.Stamp == launch.Stamp(rec.Worktree.Path) {
			continue
		}
		if err = l.unused(rec, dirs); err != nil {
			continue
		}
		if rec.Adopted {
			_, err = l.land(rec)
		} else {
			_, err = l.end(rec, "", st+", cleaned up by ym serve; ym revive "+rec.Key+" restores it")
		}
		if err == nil {
			continue
		}
		rec, _ = l.records.Find(rec.Key)
		rec.Blocked, rec.Stamp = err.Error(), launch.Stamp(rec.Worktree.Path)
		errs = append(errs, l.records.Save(rec))
		if !errors.As(err, &unlandable{}) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// goalWork reports which sessions work under a goal, none without a core to read goals from.
func (l launcher) goalWork() func(session, parent string) bool {
	if l.core == nil {
		return func(string, string) bool { return false }
	}
	work := l.core.GoalWork()
	return func(session, parent string) bool {
		_, ok := work(session, parent)
		return ok
	}
}

func (l launcher) Revive(key string) (string, error) {
	if l.core != nil && l.core.OrgRef(key) {
		return l.ReviveGoal(key)
	}
	rec, err := l.find(key)
	if err != nil {
		return l.remote(err, key, "revive")
	}
	return l.revive(rec)
}

// ReviveGoal brings back the goal or chief ref names and every session its kill ended, worktrees and work included.
func (l launcher) ReviveGoal(ref string) (string, error) {
	m, err := l.core.ReopenGoal(ref)
	if err != nil {
		return "", err
	}
	all, err := l.records.All()
	if err != nil {
		return "", err
	}
	n := 0
	var errs []error
	for _, rec := range all {
		if rec.Goal != m.ID || !rec.IsLanded() {
			continue
		}
		if _, err := l.revive(rec); err != nil {
			errs = append(errs, err)
			continue
		}
		n++
	}
	return fmt.Sprintf("revived %s with %d sessions", m.Label(), n), errors.Join(errs...)
}

func cleanDir(p string) string {
	if p == "" {
		return ""
	}
	return filepath.Clean(p)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

func reviveCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var selected string
	cmd := &cobra.Command{
		Use: "revive <key|session>",
		Short: ("Resume an exited or landed session, or one ym saw running elsewhere, in its directory, recreating the worktree and branch " +
			"when they are gone, from another clone when its own is gone, else as an empty directory holding only the conversation"),
		Args: cobra.ExactArgs(1),
		RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
			l, err := newLauncher(e.core.StateDir)
			if err != nil {
				return err
			}
			l.core = e.core
			var msg string
			if selected != "" {
				msg, err = l.ReviveProfile(args[0], selected)
			} else {
				msg, err = l.Revive(args[0])
			}
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), msg)
			return nil
		}),
	}
	cmd.Flags().StringVar(&selected, "profile", "", "resume using this subscription profile")
	return cmd
}

// unlandable is land's refusal while removing the worktree would lose work.
type unlandable struct{ key, path, reason string }

func (u unlandable) Error() string {
	return fmt.Sprintf("not landing %s: %s %s", u.key, u.path, u.reason)
}

// End closes rec's pane and removes its worktree, bundling first the work no remote holds so ym revive restores it.
func (l launcher) End(ref, goal string) (string, error) {
	rec, err := l.find(strings.TrimPrefix(ref, "launch:"))
	if err != nil {
		return l.remote(err, ref, "kill", "--yes")
	}
	if rec.IsLanded() {
		return fmt.Sprintf("%s already ended %s", rec.Key, rec.Landed.Format(time.DateTime)), nil
	}
	return l.end(rec, goal, "killed")
}

// end closes rec's pane, bundles the worktree's work no remote holds, removes the worktree and marks rec landed for why.
func (l launcher) end(rec launch.Record, goal, why string) (string, error) {
	if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
		return "", err
	}
	kept := ""
	if _, err := os.Stat(rec.Worktree.Path); err == nil && !rec.Adopted && rec.Worktree.GitDir != "" {
		if head := launch.Head(rec.Worktree.Path); head != "" {
			rec.Head = head
		}
		if rec.Recovery, err = launch.Stash(rec.Worktree.Path, rec.Key, l.recoveryDir()); err != nil {
			return "", fmt.Errorf("keep the work of %s before removing %s: %w", rec.Key, rec.Worktree.Path, err)
		}
		if err := launch.Discard(rec.Worktree); err != nil {
			return "", err
		}
		if rec.Recovery == (launch.Recovery{}) {
			launch.DropMerged(filepath.Dir(rec.Worktree.GitDir), rec.Worktree.Branch, l.squash)
		}
		if rec.Recovery != (launch.Recovery{}) {
			kept = fmt.Sprintf(", kept %s", core.Toll{Commits: rec.Recovery.Commits, Files: rec.Recovery.Files}.Work())
		}
	} else if _, err := os.Stat(rec.Worktree.GitDir); err == nil && rec.Worktree.GitDir != "" {
		if err := launch.Prune(rec.Worktree.GitDir); err != nil {
			return "", err
		}
	}
	rec.Goal = goal
	if err := l.landed(rec, why); err != nil {
		return "", err
	}
	return fmt.Sprintf("killed %s%s; ym revive %s brings it back", rec.Key, kept, rec.Key), nil
}

// Weigh is what End would take on the launched session ref names: its worktree and the work in it no remote holds.
func (l launcher) Weigh(ref string) (core.Toll, error) {
	rec, err := l.records.Lookup(strings.TrimPrefix(ref, "launch:"))
	if err != nil {
		return core.Toll{}, err
	}
	if rec.IsLanded() || rec.Adopted || rec.Worktree.GitDir == "" {
		return core.Toll{}, nil
	}
	if _, err := os.Stat(rec.Worktree.Path); err != nil {
		return core.Toll{}, nil
	}
	t, err := launch.Weigh(rec.Worktree.Path)
	return core.Toll{Worktrees: 1, Commits: t.Commits, Files: t.Files}, err
}

func (l launcher) recoveryDir() string { return filepath.Join(filepath.Dir(l.records.Dir), "recovery") }

func (l launcher) SessionStopped(id string) error {
	rec, ok := l.records.Find(id)
	if !ok || rec.IsLanded() {
		return nil
	}
	rec.Killed = time.Now()
	return l.records.Save(rec)
}

// landed keeps rec marked landed so ym revive can bring it back, and appends the landing to the landing log.
func (l launcher) landed(rec launch.Record, why string) error {
	rec.Landed = time.Now()
	if err := l.records.Save(rec); err != nil {
		return err
	}
	if l.core != nil {
		if err := l.core.StopSession(core.Dormant{Key: rec.Key, Session: rec.Session}.ID()); err != nil {
			return err
		}
	}
	if l.landings == "" {
		return nil
	}
	return launch.Log(l.landings, launch.Landing{At: rec.Landed, Key: rec.Key, Harness: rec.Harness, Session: rec.Session,
		Worktree: rec.Worktree, Head: rec.Head, Why: why})
}

// forget ends a launched session and drops its record, leaving its worktree and branch as they are.
func (l launcher) forget(rec launch.Record) {
	l.srv.Kill(tmux.SessionName(rec.Key))
	l.records.Delete(rec.Key)
	if l.core != nil {
		l.core.StopSession(core.Dormant{Key: rec.Key, Session: rec.Session}.ID())
	}
}

// unused refuses while a process in dirs works in rec's worktree.
func (l launcher) unused(rec launch.Record, dirs func() []string) error {
	if !rec.Adopted {
		if path, err := filepath.EvalSymlinks(rec.Worktree.Path); err == nil {
			if user := launch.Inside(path, dirs()); user != "" {
				return unlandable{rec.Key, rec.Worktree.Path, "has a process working in " + user}
			}
		}
	}
	return nil
}

func (l launcher) land(rec launch.Record) (string, error) {
	if rec.Adopted {
		if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
			return "", err
		}
		if err := l.landed(rec, "adopted"); err != nil {
			return "", err
		}
		return fmt.Sprintf("landed %s; %s was not ym's, so it stays", rec.Key, rec.Worktree.Path), nil
	}
	why := "worktree already gone"
	_, statErr := os.Stat(rec.Worktree.Path)
	if statErr == nil {
		g, err := launch.State(rec.Worktree.Path, l.squash)
		if err != nil {
			return "", err
		}
		ok, reason := g.Landable()
		if !ok {
			return "", unlandable{rec.Key, rec.Worktree.Path, reason}
		}
		why = reason
		if head := launch.Head(rec.Worktree.Path); head != "" {
			rec.Head = head
		}
	}
	if err := l.srv.Kill(tmux.SessionName(rec.Key)); err != nil {
		return "", err
	}
	if statErr == nil {
		if err := launch.Remove(rec.Worktree); err != nil {
			return "", err
		}
	}
	if err := l.landed(rec, why); err != nil {
		return "", err
	}
	kept := "kept"
	if rec.Worktree.GitDir != "" {
		if gone, err := launch.DropMerged(filepath.Dir(rec.Worktree.GitDir), rec.Worktree.Branch, l.squash); err != nil {
			kept = "kept: " + err.Error()
		} else if gone {
			kept = "deleted, merged into " + launch.Base(filepath.Dir(rec.Worktree.GitDir))
		}
	}
	return fmt.Sprintf("landed %s (%s); branch %s %s", rec.Key, why, rec.Worktree.Branch, kept), nil
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

// remote runs ym verb ref on the host ref names when no launch here matches it, else returns err.
func (l launcher) remote(err error, ref string, verb ...string) (string, error) {
	if l.core == nil || !errors.Is(err, core.ErrNotFound) {
		return "", err
	}
	host, rest, ok := l.core.RemoteSession(ref)
	if !ok {
		return "", err
	}
	return l.core.RunRemote(host, append(verb, rest)...)
}

func (l launcher) ReviveProfile(ref, id string) (string, error) {
	if l.core != nil {
		if host, rest, ok := l.core.RemoteSession(ref); ok {
			return l.core.RunRemote(host, "revive", rest, "--profile", id)
		}
	}
	rec, err := l.find(ref)
	if err != nil {
		return "", err
	}
	if profile.ID(rec.Profile) == profile.ID(id) {
		return l.revive(rec)
	}
	return l.SwitchProfile(ref, id)
}
