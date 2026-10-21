package core

import (
	"fmt"
	"slices"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

// KillAgent retires a chief so nothing wakes it again, then stops its running native execution and closes its workspace.
func (c *Core) KillAgent(a work.Agent, b Board) (string, error) {
	if err := c.Store.Work.RetireAgent(a.ID); err != nil {
		return "", err
	}
	signal := c.Signal
	if signal == nil {
		signal = syscall.Kill
	}
	if c.Procs != nil {
		ps, err := c.Procs()
		if err != nil {
			return "", err
		}
		for _, p := range ps {
			if i := slices.Index(p.Args, "agent-run"); i >= 0 && i+1 < len(p.Args) && p.Args[i+1] == a.ID {
				if err := signal(-p.PID, syscall.SIGTERM); err != nil {
					return "", fmt.Errorf("pid %d: %w", p.PID, err)
				}
			}
		}
	}
	if s, live := b.Session(a.Session); a.Session != "" && live {
		if _, err := c.Kill(s); err != nil {
			return "", err
		}
	}
	w, err := c.Store.Work.Get(a.Workspace)
	if err != nil {
		return "", err
	}
	if w.State != "archived" {
		if _, err = c.ChangeWork(w, work.Event{Type: "close", Actor: "owner"}); err != nil {
			return "", err
		}
	}
	return "killed " + a.Name + "; it will not wake again", nil
}
