package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/tui"
	"github.com/spf13/cobra"
)

func serveCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var once bool
	var interval time.Duration
	var discover bool
	cmd := &cobra.Command{Use: "serve", Short: "Reconcile items, pull requests and idle agents independently of the terminal", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if interval <= 0 {
			return fmt.Errorf("interval must be positive")
		}
		if !discover {
			e.core.Harnesses = nil
			e.core.Procs = func() ([]proc.Proc, error) { return nil, nil }
			e.core.PRs, e.core.Landings = nil, nil
		}
		dir, err := stateDir()
		if err != nil {
			return err
		}
		lock, err := os.OpenFile(filepath.Join(dir, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer lock.Close()
		if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
			return fmt.Errorf("work service already running: %w", err)
		}
		ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer cancel()
		upgraded := make(chan bool, 1)
		if !once {
			go monitorWorkService(ctx, cancel, dir, upgraded)
		}
		reviews := reviewPoll{fetch: intake.New().Requests, every: 5 * time.Minute}
		docs := &docSweeper{dir: dir, every: 3 * time.Minute, pace: 350 * time.Millisecond, api: os.Getenv("YAMNAYA_NOTION_API")}
		if discover && !once {
			go docs.run(ctx, e.core.Store, cmd.ErrOrStderr())
		}
		last := time.Now()
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
			err := serveCycle(e.core)
			if discover {
				err = errors.Join(err, reviews.poll(ctx, e.core), docSweepErrors(e.core.Store))
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
	cmd.Flags().BoolVar(&discover, "discover", true, "discover native sessions on this host")
	cmd.Flags().DurationVar(&interval, "interval", 5*time.Second, "background reconciliation cadence")
	return cmd
}

func serveCycle(c *core.Core) error {
	b, err := c.Board()
	if err != nil {
		return err
	}
	if _, err = c.Redeliver(b); err != nil {
		return err
	}
	var failures []error
	if _, err = c.RefreshPRs(b); err != nil {
		failures = append(failures, err)
	}
	if err = c.Brief(b); err != nil {
		failures = append(failures, err)
	}
	if err = c.RouteDocs(b); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(append(failures, c.Loop(b))...)
}

// reviewPoll asks GitHub for the owner's personal review requests at most once per every.
type reviewPoll struct {
	fetch func(context.Context) ([]intake.Request, error)
	every time.Duration
	last  time.Time
}

func (r *reviewPoll) poll(ctx context.Context, c *core.Core) error {
	if time.Since(r.last) < r.every {
		return nil
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

func ensureWorkService() error {
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
