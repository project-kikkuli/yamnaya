package core

import (
	"fmt"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// Find reads the one live session whose id is ref or starts with a ref of six or more characters, without reading the whole board.
// A session that continued under another id yields the live session it continued in.
func (c *Core) Find(ref string) (Live, error) {
	l, err := c.find(ref)
	id := ref
	if err == nil {
		if l.Status != Moved {
			return l, nil
		}
		id = l.ID
	}
	if next := c.Resolve(id); next != id {
		return c.find(next)
	}
	return l, err
}

func (c *Core) find(ref string) (Live, error) {
	procs, err := c.Procs()
	if err != nil {
		return Live{}, err
	}
	match := func(id string) bool {
		return id != "" && (id == ref || (len(ref) >= 6 && strings.HasPrefix(id, ref)))
	}
	var hit Harness
	var mine []proc.Proc
	for i, owned := range c.owned(procs) {
		for _, p := range owned {
			if match(c.Harnesses[i].SessionID(p)) {
				hit = c.Harnesses[i]
				mine = append(mine, p)
			}
		}
		if hit != nil {
			break
		}
	}
	if hit == nil {
		return Live{}, fmt.Errorf("no live session %q: %w", ref, ErrNotFound)
	}
	var found []Session
	for _, s := range hit.Discover(mine) {
		if match(s.ID) {
			found = append(found, s)
		}
	}
	if len(found) == 0 {
		return Live{}, fmt.Errorf("no live session %q: %w", ref, ErrNotFound)
	}
	l := Live{Session: found[0], Copies: found}
	for _, s := range found[1:] {
		if s.ID != l.ID {
			return Live{}, fmt.Errorf("%q matches sessions %s and %s", ref, Short(l.ID), Short(s.ID))
		}
	}
	if h, host := c.locate(l.TTY); host != nil {
		l.Handle = &h
		if l.Prompt == nil {
			if screen, err := host.Screen(h); err == nil {
				if p, ok := hit.ParsePrompt(screen); ok {
					l.Prompt = &p
				}
			}
		}
	} else {
		table := proc.Index(procs)
		l.App, l.Muxed = appOf(table, l.PID), muxed(table, l.PID)
	}
	return l, nil
}

// Settled reports whether the session sits at its prompt: idle, no dialog on screen, and no crew still working.
func (l Live) Settled() bool {
	if l.Status != Idle || l.Prompt != nil {
		return false
	}
	for _, m := range l.Crew {
		if m.Status == Busy {
			return false
		}
	}
	return true
}

// Runs reports whether pid is alive and whether it still runs session id of harness.
func (c *Core) Runs(pid int, harness, id string) (alive, same bool) {
	procs, err := c.Procs()
	if err != nil {
		return true, false
	}
	p, ok := proc.Index(procs)[pid]
	if !ok {
		return false, false
	}
	for _, h := range c.Harnesses {
		if h.Name() == harness {
			return true, h.Owns(p) && h.SessionID(p) == id
		}
	}
	return true, false
}
