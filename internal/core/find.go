package core

import (
	"errors"
	"fmt"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// Find reads one live session by full id, displayed shorthand or unambiguous prefix, without reading the whole board.
// A session that continued under another id yields the live session it continued in.
func (c *Core) Find(ref string) (Live, error) {
	l, err := c.find(ref)
	if errors.Is(err, ErrNotFound) {
		if r, remoteErr := c.remoteFind(ref); !errors.Is(remoteErr, ErrNotFound) {
			return r, remoteErr
		}
	} else if err != nil {
		return Live{}, err
	}
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
	scope := ""
	if before, after, ok := strings.Cut(ref, "@"); ok {
		scope = before
		ref = after
	}
	procs, err := c.Procs()
	if err != nil {
		return Live{}, err
	}
	var found []Session
	var ids []string
	for i, owned := range c.owned(procs) {
		var mine []proc.Proc
		for _, p := range owned {
			if SessionMatches(c.Harnesses[i].SessionID(p), ref) {
				mine = append(mine, p)
			}
		}
		for _, s := range c.Harnesses[i].Discover(mine) {
			if !c.foreign(s.Cwd) && (scope == "" || s.Harness+":"+profile.ID(s.Profile) == scope) {
				found = append(found, s)
				ids = append(ids, s.ID)
			}
		}
	}
	id, err := SessionRef(ref, ids)
	if err != nil {
		return Live{}, fmt.Errorf("no unique live session: %w", err)
	}
	var copies []Session
	for _, s := range found {
		if s.ID == id {
			if len(copies) > 0 && (copies[0].Harness != s.Harness || profile.ID(copies[0].Profile) != profile.ID(s.Profile)) {
				return Live{}, fmt.Errorf("%w %q: use harness:profile@session to choose a login", ErrAmbiguous, ref)
			}
			copies = append(copies, s)
		}
	}

	l := Live{Session: copies[0], Copies: copies}
	hit := c.harness(l.Harness)
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
