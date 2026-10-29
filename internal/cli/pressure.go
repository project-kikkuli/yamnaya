package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/slot"
	"path/filepath"
)

func pressureRoots(records []launch.Record, panes map[string]tmux.Pane, org core.Org) map[int]pressure.Owner {
	sessions, _ := pressureSources(records, org)
	roots := map[int]pressure.Owner{}
	for _, rec := range records {
		if rec.IsLanded() || !rec.Seen.IsZero() {
			continue
		}
		pane, ok := panes[tmux.SessionName(rec.Key)]
		if !ok || pane.Dead || pane.PID <= 0 {
			continue
		}
		owner := sessions[rec.Session]
		roots[pane.PID] = owner
	}
	return roots
}

// The sampler runs apart from reconciliation, which can wait on git, transcripts or remote hosts.
func watchPressure(ctx context.Context, l launcher, pool slot.Pool, w io.Writer) {
	state, err := pressure.Read(pool.Dir)
	if err != nil {
		fmt.Fprintln(w, "overload:", err)
		return
	}
	held, err := pool.Held()
	if err == nil {
		err = recordPressureNotices(l.core, &state, pool.Dir, held)
	}
	if err != nil {
		fmt.Fprintln(w, "overload:", err)
		return
	}
	var meter pressure.Meter
	sample := func() (err error) {
		at := time.Now()
		host, err := meter.Read()
		if err != nil {
			return err
		}
		before := state.Line(held)
		if held, err = pool.Held(); err != nil {
			return err
		}
		defer func() {
			if after := state.Line(held); after != before {
				fmt.Fprintln(w, "overload:", state.Status(held))
			}
		}()
		procs, err := pressure.Processes(ctx)
		if err != nil {
			return err
		}
		records, err := l.records.All()
		if err != nil {
			return err
		}
		panes, err := l.srv.Panes()
		if err != nil {
			return err
		}
		org, err := l.core.LoadOrg()
		if err != nil {
			return err
		}
		roots := pressureRoots(records, panes, org)
		sessions, dirs := pressureSources(records, org)
		table, err := proc.List()
		if err != nil {
			return err
		}
		jobs := pressure.AppJobs(procs, pressure.AppServers(table), sessions, dirs, pressure.ReadIdentity)
		sampleErr := state.Sample(at, host, procs, roots, jobs, pressure.SetNice)
		if err = state.Save(pool.Dir); err != nil {
			return errors.Join(sampleErr, err)
		}
		if err = recordPressureNotices(l.core, &state, pool.Dir, held); err != nil {
			return errors.Join(sampleErr, err)
		}
		return sampleErr
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		if err := sample(); err != nil && ctx.Err() == nil {
			fmt.Fprintln(w, "overload:", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func recordPressureNotices(c *core.Core, state *pressure.State, dir string, held int) error {
	for _, notice := range state.Notices(held) {
		key := "overload:" + state.Since.UTC().Format(time.RFC3339Nano) + ":" + notice.Goal
		if _, _, err := c.RecordGoalEvent(notice.Goal, core.EventMachineThrottled, notice.Text, key); err != nil {
			return err
		}
		state.Notified[notice.Goal] = true
		if err := state.Save(dir); err != nil {
			return err
		}
	}
	return nil
}

func pressureSources(records []launch.Record, org core.Org) (map[string]pressure.Owner, map[string]pressure.Owner) {
	parents := map[string]string{}
	for _, rec := range records {
		if rec.Session != "" {
			parents[rec.Session] = rec.Parent
		}
	}
	sessions, dirs := map[string]pressure.Owner{}, map[string]pressure.Owner{}
	for _, rec := range records {
		if rec.IsLanded() || !rec.Seen.IsZero() {
			continue
		}
		owner := pressure.Owner{Session: rec.Session}
		if m, ok := org.Managed(rec.Session, parents); ok {
			owner.Goal, owner.Manager = m.GoalID, m.Session
		}
		if rec.Session != "" {
			sessions[rec.Session] = owner
		}
		if rec.Worktree.Path != "" {
			dirs[filepath.Clean(rec.Worktree.Path)] = owner
		}
	}
	return sessions, dirs
}
