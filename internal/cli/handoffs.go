package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
	"github.com/spf13/cobra"
)

func handoffCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	root := &cobra.Command{Use: "handoff", Short: "Inspect durable agent handoff capture"}
	root.AddCommand(&cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		status, err := e.core.Store.Handoffs.Status()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	})}, &cobra.Command{Use: "resume", Short: "Reconsider unresolved handoffs after correcting the reported capture failure", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error { return e.core.Store.Handoffs.Resume() })})
	root.AddCommand(&cobra.Command{Use: "scan", Short: "Capture live sessions' transcripts and reconcile extracted requests", Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) error {
		b, err := e.core.Board()
		if err != nil {
			return err
		}
		if err := e.core.CaptureHandoffs(b); err != nil {
			return err
		}
		if err := applyHandoffs(e.core, b); err != nil {
			return err
		}
		status, err := e.core.Store.Handoffs.Status()
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
	})})
	return root
}

func reconcileHandoffs(c *core.Core, b core.Board) error {
	captureErr := c.CaptureHandoffs(b)
	if err := applyHandoffs(c, b); err != nil {
		return err
	}
	batch, err := c.Store.Handoffs.Batch()
	if err != nil || len(batch) == 0 {
		return errors.Join(captureErr, err)
	}
	dir, err := stateDir()
	if err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(dir, "handoff-run.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == syscall.EWOULDBLOCK {
		return captureErr
	}
	if err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	log, err := os.OpenFile(filepath.Join(dir, "handoff-run.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	run := exec.Command(self, "handoff-run")
	run.Env = append(os.Environ(), "YAMNAYA_HANDOFF_SUPERVISED=1")
	run.ExtraFiles = []*os.File{lock}
	run.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	run.Stdout, run.Stderr = log, log
	if err = run.Start(); err != nil {
		return err
	}
	return errors.Join(captureErr, run.Process.Release())
}

func handoffRunCmd(with func(runFunc) func(*cobra.Command, []string) error) *cobra.Command {
	return &cobra.Command{Use: "handoff-run", Hidden: true, Args: cobra.NoArgs, RunE: with(func(e *env, cmd *cobra.Command, args []string) (result error) {
		if os.Getenv("YAMNAYA_HANDOFF_SUPERVISED") != "1" {
			return fmt.Errorf("handoff-run requires the background supervisor")
		}
		lock := os.NewFile(3, "handoff lock")
		defer lock.Close()
		events, err := e.core.Store.Handoffs.Batch()
		if err != nil || len(events) == 0 {
			return err
		}
		defer func() {
			if result != nil {
				if saveErr := e.core.Store.Handoffs.Fail(events, result); saveErr != nil {
					result = saveErr
				}
			}
		}()
		ledger, err := handoffLedger(e.core, events[0])
		if err != nil {
			return err
		}
		dir, err := stateDir()
		if err != nil {
			return err
		}
		n := handoff.Native{Directory: dir}
		if bin, lookup := exec.LookPath("claude"); lookup == nil {
			n.Harness, n.Binary = "claude", bin
		} else if bin, lookup = exec.LookPath("codex"); lookup == nil {
			n.Harness, n.Binary = "codex", bin
		} else {
			err = fmt.Errorf("no native Claude or Codex classifier is installed")
		}
		var claims []handoff.Claim
		if err == nil {
			ctx, cancel := context.WithTimeout(cmd.Context(), 2*time.Minute)
			defer cancel()
			claims, err = n.Classify(ctx, events, ledger)
		}
		if err != nil {
			return err
		}
		return e.core.Store.Handoffs.Classified(events, claims)
	})}
}
