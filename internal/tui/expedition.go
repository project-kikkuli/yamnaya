package tui

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

type expedition struct {
	id, session, mountain string
	status                core.Status
	cut                   bool
	action                time.Time
	tools                 int
}

func expeditions(b core.Board) map[string]expedition {
	out := map[string]expedition{}
	add := func(id, session, mountain string, status core.Status, cut bool, facts core.Facts) {
		n := 0
		for _, count := range facts.Tools {
			n += count
		}
		out[id] = expedition{id: id, session: session, mountain: mountain, status: status, cut: cut, action: facts.Doing.At, tools: n}
	}
	live := map[string]bool{}
	for _, p := range b.Projects {
		for _, s := range p.Sessions {
			live[s.Key()] = true
		}
	}
	for _, p := range b.Projects {
		for _, s := range p.Sessions {
			add(s.Key(), s.Key(), p.Key, s.Status, false, s.Facts)
			for _, c := range s.Crew {
				if !live[c.ID] {
					add(s.Key()+"/"+c.ID, s.Key(), p.Key, c.Status, c.Cut, c.Facts)
				}
			}
		}
	}
	return out
}
