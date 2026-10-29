package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/slot"
	"github.com/spf13/cobra"
)

// exitStatus is a command's own exit code, which ym exits with without printing anything more.
type exitStatus int

func (e exitStatus) Error() string { return fmt.Sprintf("exit status %d", int(e)) }

func slotCmd() *cobra.Command {
	var status, asJSON bool
	var nice int
	var lane string
	cmd := &cobra.Command{
		Use:   "slot [--nice N] [--lane NAME] -- <command> [args...] | slot --status [--json]",
		Short: "Run a heavy command (tests, builds, jobs) once one of the machine's shared slots is free, niced, in its own process group",
		Long: "slot waits for one of the machine-wide slots agent-slot also uses (capacity from physical cores and memory), " +
			"runs the command at niceness --nice while holding it, and releases it when the command and everything it left in its process group have ended. " +
			"Common runtime and build worker pools share the available cores across slots. " +
			"--lane serializes commands with the same name before they take a slot. " +
			"An interrupt or termination passes to the whole group. --status lists capacity, load, holders and waiters.",
		RunE: func(cmd *cobra.Command, args []string) error {
			pool, err := slot.Machine()
			if err != nil {
				return err
			}
			if status {
				st, err := pool.Status()
				if err != nil {
					return err
				}
				load, _ := slot.Load()
				if asJSON {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(struct {
						slot.Status
						Load float64 `json:"load"`
					}{st, load})
				}
				printSlots(cmd.OutOrStdout(), st, load, time.Now())
				return nil
			}
			if len(args) == 0 {
				return errors.New("name a command: ym slot -- <command> [args...]")
			}
			c := slot.Claim{PID: os.Getpid(), Command: args, Since: time.Now(), Lane: lane}
			c.Cwd, _ = os.Getwd()
			if caller, ok := core.CallerFromEnv(os.Getenv); ok {
				c.Harness, c.Session = caller.Harness, caller.Session
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
			defer stop()
			leave, err := pool.Lane(ctx, lane, c, func() {
				fmt.Fprintf(cmd.ErrOrStderr(), "ym slot: lane %q taken, waiting (ym slot --status shows who holds it)\n", lane)
			})
			if errors.Is(err, context.Canceled) {
				return exitStatus(130)
			}
			if err != nil {
				return err
			}
			defer leave()
			release, n, err := pool.Acquire(ctx, c, func() {
				st, _ := pool.Status()
				if st.Pressure.Holding && len(st.Holders) > 0 {
					fmt.Fprintln(cmd.ErrOrStderr(), "ym slot: overload holds new slots, waiting (ym slot --status shows load and consumers)")
				} else {
					fmt.Fprintf(cmd.ErrOrStderr(), "ym slot: all %d slots taken, waiting (ym slot --status shows who holds them)\n", st.Capacity)
				}
			})
			stop()
			if errors.Is(err, context.Canceled) {
				return exitStatus(130)
			}
			if err != nil {
				return err
			}
			defer release()
			fmt.Fprintf(cmd.ErrOrStderr(), "ym slot: acquired %d/%d, %d workers\n", n, pool.Capacity, pool.Workers())
			code, err := slot.Run(args, nice, slot.Environment(os.Environ(), pool.Workers()))
			if err != nil {
				return err
			}
			if code != 0 {
				return exitStatus(code)
			}
			return nil
		},
	}
	cmd.Flags().SetInterspersed(false)
	cmd.Flags().BoolVar(&status, "status", false, "list capacity, load, holders and waiters")
	cmd.Flags().BoolVar(&asJSON, "json", false, "with --status, print JSON")
	cmd.Flags().IntVar(&nice, "nice", 10, "niceness to run the command at; a higher one it already has is kept")
	cmd.Flags().StringVar(&lane, "lane", "", "serialize commands with this lane name across the machine")
	return cmd
}

func printSlots(w io.Writer, st slot.Status, load float64, now time.Time) {
	fmt.Fprintf(w, "%d slots (%d cores, %d GiB RAM, %d workers/slot), %d held, %d waiting, load %.1f\n", st.Capacity, st.Cores, st.Memory>>30, st.Workers, len(st.Holders), len(st.Waiters), load)
	if line := st.Pressure.Line(len(st.Holders)); line != "" {
		fmt.Fprintln(w, line)
	}
	for _, h := range st.Holders {
		fmt.Fprintf(w, "  slot %d  %s\n", h.Slot, claimLine(h, now))
	}
	for _, c := range st.Waiters {
		fmt.Fprintf(w, "  waiting  %s\n", claimLine(c, now))
	}
}

func claimLine(c slot.Claim, now time.Time) string {
	parts := []string{clip(c.Line(), 80)}
	if c.Lane != "" {
		parts = append(parts, "lane "+c.Lane)
	}
	if c.PID > 0 {
		parts = append(parts, fmt.Sprintf("pid %d", c.PID))
	}
	if c.Session != "" {
		parts = append(parts, c.Harness+" "+core.Short(c.Session))
	}
	if !c.Since.IsZero() {
		parts = append(parts, "for "+span(now.Sub(c.Since)))
	}
	if c.Cwd != "" {
		parts = append(parts, "in "+c.Cwd)
	}
	return strings.Join(parts, "  ")
}

func span(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}
