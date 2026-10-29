package core

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/project-kikkuli/yamnaya/internal/pressure"
)

// Throttle stops (on) or continues (off) every local session ym runs under t, or every one when t is nil, with each
// process under it. The session calling it is never stopped, so an agent that throttles can still unthrottle.
func (c *Core) Throttle(b Board, t *OffTarget, on bool) (string, error) {
	if c.Slots == nil {
		return "", errors.New("throttle: this ym has no machine state")
	}
	pool, err := c.Slots()
	if err != nil {
		return "", err
	}
	state, err := pressure.ReadThrottle(pool.Dir)
	if err != nil {
		return "", err
	}
	in := func(string) bool { return true }
	scope := "every ym agent"
	if t != nil {
		targets := map[string]bool{t.key(): true}
		in = func(session string) bool { return offIn(targets, b.Org, b.parents, c.Resolve(session)) }
		scope = t.String()
	}
	list := c.Processes
	if list == nil {
		list = func() ([]pressure.Process, error) { return pressure.Processes(context.Background()) }
	}
	signal := c.ThrottleSignal
	if signal == nil {
		signal = pressure.Signal
	}
	if !on {
		n, err := state.Resume(c.now(), in, signal)
		if saveErr := state.Save(pool.Dir); saveErr != nil {
			return "", errors.Join(err, saveErr)
		}
		return fmt.Sprintf("resumed %d processes for %s; %d agents still paused", n, scope, len(state.Sessions())), err
	}
	procs, err := list()
	if err != nil {
		return "", err
	}
	self := map[int]bool{}
	parent := map[int]int{}
	for _, p := range procs {
		parent[p.PID] = p.PPID
	}
	for id := os.Getpid(); id > 1 && !self[id]; id = parent[id] {
		self[id] = true
	}
	roots := map[int]string{}
	for _, s := range b.Sessions {
		if s.Remote != "" || !s.Ours || s.PID <= 0 || self[s.PID] || !in(s.Key()) {
			continue
		}
		roots[s.PID] = s.Key()
	}
	n, err := state.Pause(c.now(), list, roots, signal)
	if saveErr := state.Save(pool.Dir); saveErr != nil {
		return "", errors.Join(err, saveErr)
	}
	return fmt.Sprintf("paused %d agents (%d processes) for %s; Z on the board or ym unthrottle resumes them", len(roots), n, scope), err
}
