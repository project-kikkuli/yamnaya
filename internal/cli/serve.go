package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/tui"
	"github.com/spf13/cobra"
)

func serveCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	var once bool
	var interval time.Duration
	var discover bool
	cmd := &cobra.Command{Use: "serve", Short: "Reconcile items, pull requests and handoffs independently of the terminal", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		if interval <= 0 {
			return fmt.Errorf("interval must be positive")
		}
		if !discover {
			e.core.Harnesses = nil
			e.core.Procs = func() ([]proc.Proc, error) { return nil, nil }
			e.core.PRs = nil
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
		for {
			err := serveCycle(e.core)
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
	if err = reconcileHandoffs(c, b); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
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
