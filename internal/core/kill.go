package core

import (
	"fmt"
	"strings"
	"syscall"
	"time"
)

// Kill sends SIGTERM to each process running s and waits for them to end. Its terminal, worktree and transcript stay.
func (c *Core) Kill(s Live) (string, error) {
	signal := c.Signal
	if signal == nil {
		signal = syscall.Kill
	}
	grace := c.KillGrace
	if grace == 0 {
		grace = 3 * time.Second
	}
	pids := []int{s.PID}
	if len(s.Copies) > 0 {
		pids = nil
		for _, cp := range s.Copies {
			pids = append(pids, cp.PID)
		}
	}
	var sent []int
	for _, pid := range pids {
		alive, same := c.Runs(pid, s.Harness, s.ID)
		if !alive {
			continue
		}
		if !same {
			return "", fmt.Errorf("pid %d no longer runs %s %s", pid, s.Harness, Short(s.Key()))
		}
		if err := signal(pid, syscall.SIGTERM); err != nil {
			return "", fmt.Errorf("pid %d: %w", pid, err)
		}
		sent = append(sent, pid)
	}
	if len(sent) == 0 {
		return fmt.Sprintf("%s %s already ended", s.Harness, Short(s.Key())), nil
	}
	var left []string
	for end := time.Now().Add(grace); ; time.Sleep(50 * time.Millisecond) {
		left = left[:0]
		for _, pid := range sent {
			if alive, same := c.Runs(pid, s.Harness, s.ID); alive && same {
				left = append(left, fmt.Sprint(pid))
			}
		}
		if len(left) == 0 || !time.Now().Before(end) {
			break
		}
	}
	if len(left) > 0 {
		return "", fmt.Errorf("%s %s still running (pid %s) after SIGTERM", s.Harness, Short(s.Key()), strings.Join(left, ", "))
	}
	if c.Store != nil {
		if err := c.Store.RecordKill(s.Key(), c.now()); err != nil {
			return "", fmt.Errorf("killed %s %s; recording exit: %w", s.Harness, Short(s.Key()), err)
		}
	}
	return fmt.Sprintf("killed %s %s", s.Harness, Short(s.Key())), nil
}

// Label names a session for a confirmation, such as "claude 06456f12 (Allergies rollout)".
func (s Live) Label() string {
	label := s.Harness + " " + Short(s.Key())
	if s.Title != "" {
		label += " (" + s.Title + ")"
	}
	return label
}
