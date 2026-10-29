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
	if c.ThrottleDir == nil {
		return "", errors.New("throttle: this ym keeps no throttle")
	}
	dir, err := c.ThrottleDir()
	if err != nil {
		return "", err
	}
	state, err := pressure.ReadThrottle(dir)
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
		if t == nil {
			state.Blanket = false
		}
		if !state.On() {
			state.Since = timeZero
		}
		if saveErr := state.Save(dir); saveErr != nil {
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
	if t == nil {
		state.Blanket = true
		if state.Since.IsZero() {
			state.Since = c.now()
		}
	}
	if saveErr := state.Save(dir); saveErr != nil {
		return "", errors.Join(err, saveErr)
	}
	held := ""
	if t == nil {
		held = "; ym serve resumes and relaunches none until then"
	}
	return fmt.Sprintf("paused %d agents (%d processes) for %s; Q on the board or ym unthrottle resumes them%s", len(roots), n, scope, held), err
}

// markThrottled reads the owner's throttle into b and flags each session it holds stopped.
func (c *Core) markThrottled(b *Board) {
	if c.ThrottleDir == nil {
		return
	}
	dir, err := c.ThrottleDir()
	if err != nil {
		return
	}
	if b.throttle, err = pressure.ReadThrottle(dir); err != nil {
		return
	}
	mark := func(ls []Live) {
		for i := range ls {
			ls[i].Paused, ls[i].Resumed = b.throttle.Has(ls[i].Key()), b.throttle.Ended
		}
	}
	mark(b.Sessions)
	for i := range b.Projects {
		mark(b.Projects[i].Sessions)
	}
}
