package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/tui"
	"github.com/spf13/cobra"
)

func serveCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var once bool
	var interval time.Duration
	var discover bool
	var briefs string
	var wait bool
	cmd := &cobra.Command{Use: "serve", Short: "Reconcile items, pull requests and idle agents independently of the terminal", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) (err error) {
		os.Unsetenv(tui.RestartedEnv)
		var log *serveLog
		if dir, dirErr := stateDir(); dirErr == nil {
			if log = openServeLog(cmd.ErrOrStderr(), filepath.Join(dir, "work-service.log")); log != nil {
				cmd.SetOut(log)
				cmd.SetErr(log)
				defer func() {
					if err != nil && !errors.Is(err, tui.ErrRestart) {
						fmt.Fprintln(log, "ym:", err)
						err = loggedError{err}
					}
					log.Close()
				}()
			}
		}
		if interval <= 0 {
			return fmt.Errorf("interval must be positive")
		}
		e.core.Slots = nil
		if !discover {
			e.core.Harnesses = nil
			e.core.Procs = func() ([]proc.Proc, error) { return nil, nil }
			e.core.PRs, e.core.Landings = nil, nil
		}
		dir, err := stateDir()
		if err != nil {
			return err
		}
		lockPath := filepath.Join(dir, "work-service.lock")
		lock := inheritedServeLock(lockPath)
		if lock == nil {
			if lock, err = os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600); err != nil {
				return err
			}
			how := syscall.LOCK_EX | syscall.LOCK_NB
			if wait {
				how = syscall.LOCK_EX
			}
			if err = syscall.Flock(int(lock.Fd()), how); err != nil {
				lock.Close()
				if log != nil {
					return nil
				}
				return fmt.Errorf("work service already running: %w", err)
			}
		}
		defer lock.Close()
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		upgraded := make(chan bool, 1)
		if !once {
			go monitorWorkService(ctx, cancel, dir, upgraded)
		}
		reviews := reviewPoll{fetch: intake.New().Requests, every: 5 * time.Minute}
		docs := &docSweeper{dir: dir, every: 3 * time.Minute, recheck: 15 * time.Minute, pace: 350 * time.Millisecond, api: os.Getenv("YAMNAYA_NOTION_API")}
		if discover && !once {
			go docs.run(ctx, e.core.Store, cmd.ErrOrStderr())
		}
		last := time.Now()
		site := &briefSite{core: e.core}
		if briefs != "" && !once {
			go func() {
				if err := site.listen(ctx, briefs); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), err)
				}
			}()
		}
		for {
			if at := time.Now(); at.Round(0).Sub(last.Round(0))-at.Sub(last) > time.Minute {
				e.core.Woke = at
			}
			last = time.Now()
			if discover && once {
				if err := docs.sweep(ctx, e.core.Store); err != nil {
					return err
				}
			}
			err := serveCycle(e.core, site.observe, func(b core.Board) {
				if err := writeSnapshot(e.core, dir, b); err != nil {
					fmt.Fprintln(cmd.ErrOrStderr(), "board snapshot:", err)
				}
			})
			if discover {
				err = errors.Join(err, reviews.poll(ctx, e.core), docSweepErrors(e.core.Store))
			}
			if abandoned(dir) {
				return endAbandoned(e.core, dir, cmd.ErrOrStderr())
			}
			message := ""
			if err != nil {
				message = err.Error()
			}
			raw, _ := json.Marshal(map[string]any{"pid": os.Getpid(), "at": time.Now().UTC(), "error": message})
			if writeErr := os.WriteFile(filepath.Join(dir, "work-service.json.new"), raw, 0600); writeErr != nil {
				return writeErr
			}
			if writeErr := os.Rename(filepath.Join(dir, "work-service.json.new"), filepath.Join(dir, "work-service.json")); writeErr != nil {
				return writeErr
			}
			if once {
				return err
			}
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), err)
			}
			timer := time.NewTimer(interval)
			select {
			case <-ctx.Done():
				timer.Stop()
				select {
				case changed := <-upgraded:
					if changed {
						if err := keepServeLockAcrossExec(lock); err != nil {
							fmt.Fprintln(cmd.ErrOrStderr(), "ym serve restarts without its lock:", err)
						}
						return tui.ErrRestart
					}
				default:
				}
				return nil
			case <-timer.C:
			}
		}
	})}
	cmd.Flags().BoolVar(&once, "once", false, "reconcile once and exit")
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for a ym serve already running to exit, then take over, instead of exiting")
	cmd.Flags().BoolVar(&discover, "discover", true, "discover native sessions on this host")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "background reconciliation cadence")
	cmd.Flags().StringVar(&briefs, "briefs", core.BriefsAddr(), "address to serve goal and chief briefs on; empty serves none")
	return cmd
}

// serveCycle is one pass of ym serve; each of seen gets the board it worked from.
func serveCycle(c *core.Core, seen ...func(core.Board)) error {
	b, err := c.Board()
	if err != nil {
		return err
	}
	for _, f := range seen {
		f(b)
	}
	if _, err = c.Redeliver(b); err != nil {
		return err
	}
	var failures []error
	if _, err = c.RefreshPRs(b); err != nil {
		failures = append(failures, err)
	}
	if err = c.RouteDocs(b); err != nil {
		failures = append(failures, err)
	}
	if l, ok := c.Launches.(launcher); ok {
		failures = append(failures, l.unstall(time.Now()))
	}
	if err = relayHosts(c); err != nil {
		failures = append(failures, err)
	}
	failures = append(failures, c.Loop(b))
	return errors.Join(append(failures, c.Brief(b))...)
}

// reviewPoll asks GitHub for the owner's personal review requests at most once per every.
type reviewPoll struct {
	fetch func(context.Context) ([]intake.Request, error)
	every time.Duration
	last  time.Time
}

// poll reads the owner's review requests on the owner's own host only: a host whose relays another host carries leaves them to it.
func (r *reviewPoll) poll(ctx context.Context, c *core.Core) error {
	if time.Since(r.last) < r.every {
		return nil
	}
	if self, err := c.Store.RelaySelf(); err != nil || self.Courier != "" {
		return err
	}
	r.last = time.Now()
	reqs, err := r.fetch(ctx)
	if err != nil {
		return err
	}
	out := make([]core.ReviewRequest, len(reqs))
	for i, q := range reqs {
		out[i] = core.ReviewRequest{URL: q.URL, Updated: q.Updated}
	}
	return c.Store.SyncReviews(out)
}

// ensureService starts ym serve when its lock is free; a variable so tests can watch it.
var ensureService = ensureWorkService

// keepServing restarts ym serve from any ym command once a state dir has had one, so a serve that died comes back the next time
// an agent or the owner runs ym, board or not.
func keepServing(w io.Writer) {
	dir, err := stateDir()
	if err != nil {
		return
	}
	if _, err := os.Stat(filepath.Join(dir, "work-service.lock")); err != nil {
		return
	}
	if err := ensureService(); err != nil {
		fmt.Fprintln(w, "ym serve is down and did not restart:", err)
	}
}

// ensureWorkService starts ym serve unless it runs, or YAMNAYA_NO_SERVE=1 keeps this command from starting it.
func ensureWorkService() error {
	if os.Getenv("YAMNAYA_NO_SERVE") == "1" {
		return nil
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "work-start.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		return nil
	}
	return startWorkService()
}

func startWorkService() error {
	dir, err := stateDir()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil
	}
	lock.Close()
	self, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "work-service.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	p := exec.Command(self, "serve")
	p.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	p.Stdout, p.Stderr = log, log
	if err = p.Start(); err != nil {
		return err
	}
	return p.Process.Release()
}

// abandoned reports whether dir is a state directory other than the owner's that was deleted, as a finished rehearsal deletes its own.
func abandoned(dir string) bool {
	home, err := os.UserHomeDir()
	if abs, _ := filepath.Abs(dir); err != nil || abs == filepath.Join(home, ".local", "state", "yamnaya") {
		return false
	}
	_, err = os.Stat(dir)
	return errors.Is(err, fs.ErrNotExist)
}

// endAbandoned ends the agents ym launched for a deleted state directory, unless they share the owner's tmux server, then stops serving it.
func endAbandoned(c *core.Core, dir string, log io.Writer) error {
	l, ok := c.Launches.(launcher)
	if !ok || l.srv.Socket == tmux.Server {
		fmt.Fprintf(log, "%s was deleted; ym serve stops\n", dir)
		return nil
	}
	fmt.Fprintf(log, "%s was deleted; ym serve ends tmux -L %s and its agents, then stops\n", dir, l.srv.Socket)
	return l.srv.KillServer()
}
