package disk

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Report is what one step did: Skipped says why it did nothing, Did what it did or with a dry run would do.
type Report struct {
	Step    string
	Did     string
	Skipped string
	Freed   int64
	Err     error
	// Quiet leaves it out of gc.log: nothing went, or the step logged each removal itself.
	Quiet bool
	// Blocked says the step wanted to free space and something running stopped it, so its cache is still there.
	Blocked bool
}

// Step is one thing a pass tries, run by every pass at Tier or higher, and at no higher than Until when that is set.
type Step struct {
	Name  string
	Tier  int
	Until int
	Run   func(context.Context) Report
}

// Run runs the steps of tier and below in order until enough reports true, reporting each. complete says every step ran, so
// the disk holds what a pass could not free.
func Run(ctx context.Context, steps []Step, tier int, enough func() bool, report func(Report)) (complete bool) {
	for _, s := range steps {
		if s.Tier > tier || s.Until > 0 && tier > s.Until {
			continue
		}
		if ctx.Err() != nil || enough() {
			return false
		}
		r := s.Run(ctx)
		r.Step = s.Name
		report(r)
	}
	return ctx.Err() == nil
}

// Env is what the cache steps read the machine through.
type Env struct {
	Dry   bool
	Force bool
	Home  string
	// Paths are the watched paths whose filesystems say when the pass is done.
	Paths []string
	// Dirs are repositories, for a tool that only runs inside a project.
	Dirs []string
	// May reports whether this ym may touch a path, and with "" a cache with no path of its own. Nil allows everything.
	May  func(path string) bool
	Stat func(string) (Usage, error)
	Now  func() time.Time
	// Find locates a tool, or "" when it is not installed.
	Find func(name string) string
	// Exec runs a tool in dir, killing it after limit.
	Exec func(ctx context.Context, dir string, limit time.Duration, argv ...string) (string, error)
	// Holders are the paths live processes hold: their working directories, open files and arguments.
	Holders func() []string
	// Running reports whether a process runs a program; nil says none does.
	Running func(name string) bool
}

// Machine fills an Env's unset readers from the real machine.
func (e Env) Machine() Env {
	if e.Stat == nil {
		e.Stat = Stat
	}
	if e.Now == nil {
		e.Now = time.Now
	}
	if e.Find == nil {
		e.Find = func(name string) string { return Find(e.Home, name) }
	}
	if e.Exec == nil {
		e.Exec = Command
	}
	if e.Holders == nil {
		e.Holders = Holders
	}
	if e.Running == nil {
		e.Running = Running
	}
	return e
}

// Enough reports whether every watched filesystem reached TargetFree; a forced pass is never done early.
func (e Env) Enough() bool {
	if e.Force {
		return false
	}
	low, ok := Lowest(e.Stat, e.Paths)
	return !ok || low.Free() >= TargetFree
}

// Needed reports whether freeing path helps: a forced pass always, else its filesystem is short of TargetFree.
func (e Env) Needed(path string) bool {
	if e.Force {
		return true
	}
	u, err := e.Stat(path)
	return err != nil || u.Free() < TargetFree
}

func (e Env) may(path string) bool { return e.May == nil || e.May(path) }

// Find locates a tool on PATH, else among mise's shims, which a service's PATH often lacks.
func Find(home, name string) string {
	if path, err := exec.LookPath(name); err == nil {
		return path
	}
	shim := filepath.Join(home, ".local", "share", "mise", "shims", name)
	if fi, err := os.Stat(shim); err == nil && !fi.IsDir() {
		return shim
	}
	return ""
}

// Command runs argv in dir for at most limit and returns its output.
func Command(ctx context.Context, dir string, limit time.Duration, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.WaitDelay = 5 * time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return strings.TrimSpace(string(out)), err
}
