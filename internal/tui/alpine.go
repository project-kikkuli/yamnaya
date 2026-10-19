package tui

import (
	"maps"
	"math"
	"slices"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const alpineInterval = 100 * time.Millisecond

type alpine struct {
	frame     uint64
	ready     bool
	hidden    bool
	mountains []massif
	teams     map[string]expedition
	kills     map[string]time.Time
	events    []alpineEvent
}

func (a *alpine) observe(b core.Board) {
	teams := expeditions(b)
	active := map[string]core.Project{}
	for _, p := range b.Projects {
		if p.Shown() {
			active[p.Key] = p
		}
	}
	for i := range a.mountains {
		p := &a.mountains[i]
		v, ok := active[p.key]
		p.active, p.needs = ok, 0
		if ok {
			p.name = v.Name
			delete(active, p.key)
		}
	}
	for _, key := range slices.Sorted(maps.Keys(active)) {
		p := active[key]
		a.mountains = append(a.mountains, massif{key: key, name: p.Name, active: true})
	}
	for _, n := range b.Needs {
		for i := range a.mountains {
			if a.mountains[i].key == n.Project {
				a.mountains[i].needs++
			}
		}
	}
	if a.ready {
		for id, at := range b.Kills {
			if !at.After(a.kills[id]) {
				continue
			}
			if lead, ok := a.teams[id]; ok {
				people := 0
				for _, e := range a.teams {
					if e.session == id && e.status != core.Done {
						people++
					}
				}
				a.events = append(a.events, alpineEvent{kind: "storm", mountain: lead.mountain, team: id, frame: a.frame, people: max(1, people)})
			}
		}
		for id, e := range teams {
			old, existed := a.teams[id]
			kind := ""
			switch {
			case !existed:
				kind = "depart"
			case e.cut && !old.cut:
				kind = "fall"
			case e.status == core.Done && old.status != core.Done && !e.cut:
				kind = "summit"
			case e.tools > old.tools || e.action.After(old.action):
				kind = "work"
			}
			if kind == "" {
				continue
			}
			a.events = append(a.events, alpineEvent{kind: kind, mountain: e.mountain, team: id, frame: a.frame})
			if kind == "work" {
				for i := range a.mountains {
					if a.mountains[i].key == e.mountain {
						a.mountains[i].relief = math.Min(1, a.mountains[i].relief+0.12)
					}
				}
			}
		}
	}
	a.teams, a.kills, a.ready = teams, maps.Clone(b.Kills), true
}

func (a *alpine) advance() {
	a.frame++
	a.events = slices.DeleteFunc(a.events, func(e alpineEvent) bool { return a.frame-e.frame >= e.life() })
	for i := range a.mountains {
		a.mountains[i].relief *= 0.998
	}
}

func (m Model) alpineHeight() int {
	if m.alpine.hidden || m.width < 60 || m.height < 28 {
		return 0
	}
	return min(14, m.height/4)
}

func alpineTick() tea.Cmd {
	return tea.Tick(alpineInterval, func(time.Time) tea.Msg { return alpineFrame{} })
}

type alpineFrame struct{}
