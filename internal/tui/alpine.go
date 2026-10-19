package tui

import (
	"maps"
	"math"
	"slices"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

const alpineInterval = 100 * time.Millisecond

type alpine struct {
	frame         uint64
	ready, hidden bool
	mountains     []massif
	index         map[string]int
	teams         map[string]expedition
	groups        map[string]*expeditionGroup
	effects       map[string][]alpineEvent
	kills         map[string]time.Time
	events        []alpineEvent
	eventIndex    map[string]int
	hues          map[string]int
}

func (a *alpine) observe(b core.Board) {
	teams := expeditions(b)
	a.events = slices.DeleteFunc(a.events, func(e alpineEvent) bool { return a.frame-e.frame >= e.life() })
	a.eventIndex = map[string]int{}
	for i, e := range a.events {
		a.eventIndex[e.kind+"\x00"+e.mountain] = i
	}
	keys := make([]string, len(b.Projects))
	for i, p := range b.Projects {
		keys[i] = p.Key
	}
	a.hues = hueSlots(keys)

	if a.index == nil {
		a.index = map[string]int{}
	}
	for i := range a.mountains {
		a.mountains[i].active = false
		a.mountains[i].needs = 0
	}
	for _, p := range b.Projects {
		if !p.Shown() {
			continue
		}
		i, ok := a.index[p.Key]
		if !ok {
			i = len(a.mountains)
			a.index[p.Key] = i
			a.mountains = append(a.mountains, massif{key: p.Key})
		}
		m := &a.mountains[i]
		m.name, m.region, m.active = p.Name, p.Repo, true
	}
	for _, n := range b.Needs {
		if i, ok := a.index[n.Project]; ok {
			a.mountains[i].needs++
		}
	}
	if a.ready {
		for id, at := range b.Kills {
			if !at.After(a.kills[id]) {
				continue
			}
			if lead, ok := a.teams[id]; ok {
				people := 1
				if g := a.groups["s:"+id]; g != nil {
					people = max(1, g.total()-g.counts[core.Done])
				}
				a.emit(alpineEvent{kind: "storm", mountain: lead.mountain, team: id, frame: a.frame, people: people})
			}
		}
		for id, e := range teams {
			old, existed := a.teams[id]
			kind := ""
			switch {
			case !existed && e.status != core.Done:
				kind = "depart"
			case e.cut && !old.cut:
				kind = "fall"
			case existed && e.status == core.Done && old.status != core.Done && !e.cut:
				kind = "summit"
			case existed && (e.tools > old.tools || e.action.After(old.action)):
				kind = "work"
			}
			if kind == "" {
				continue
			}
			a.emit(alpineEvent{kind: kind, mountain: e.mountain, team: id, frame: a.frame, people: 1})
			if kind == "work" {
				if i, ok := a.index[e.mountain]; ok {
					p := &a.mountains[i]
					p.relief = math.Min(1, p.height(a.frame)+0.12)
					p.frame = a.frame
				}
			}
		}
	}
	a.teams, a.kills, a.ready = teams, maps.Clone(b.Kills), true
	a.group()
	for _, p := range b.Projects {
		for _, s := range p.Sessions {
			for _, member := range s.Crew {
				key := "a:" + s.Key() + "/" + member.ID
				if a.groups[key] == nil {
					if group := a.groups["a:"+member.ID]; group != nil {
						a.groups[key] = group
					}
				}
			}
		}
	}
}

func (a *alpine) emit(e alpineEvent) {
	key := e.kind + "\x00" + e.mountain
	if i, ok := a.eventIndex[key]; ok {
		e.people += a.events[i].people
		a.events[i] = e
		return
	}
	a.eventIndex[key] = len(a.events)
	a.events = append(a.events, e)
}

func (a *alpine) group() {
	a.groups = map[string]*expeditionGroup{}
	all := slices.Collect(maps.Values(a.teams))
	slices.SortFunc(all, func(a, b expedition) int {
		if rank := expeditionRank(a) - expeditionRank(b); rank != 0 {
			return rank
		}
		return strings.Compare(a.id, b.id)
	})
	for _, e := range all {
		for scale, key := range []string{"r:" + e.region, "m:" + e.mountain, "s:" + e.session, "a:" + e.id} {
			g := a.groups[key]
			if g == nil {
				g = &expeditionGroup{region: e.region, mission: e.mountain, scale: scale}
				a.groups[key] = g
			}
			g.add(e)
		}
	}
	for _, p := range a.mountains {
		if a.groups["m:"+p.key] == nil {
			a.groups["m:"+p.key] = &expeditionGroup{region: p.region, mission: p.key, scale: 1}
		}
		if a.groups["r:"+p.region] == nil {
			a.groups["r:"+p.region] = &expeditionGroup{region: p.region}
		}
		a.groups["m:"+p.key].needs = p.needs
		a.groups["r:"+p.region].needs += p.needs
	}
	a.effects = map[string][]alpineEvent{}
	for _, e := range a.events {
		p := a.mountains[a.index[e.mountain]]
		keys := []string{"r:" + p.region, "m:" + e.mountain, "a:" + e.team}
		if team, ok := a.teams[e.team]; ok {
			keys = append(keys, "s:"+team.session)
		} else {
			keys = append(keys, "s:"+e.team)
		}
		for _, key := range keys {
			effects := a.effects[key]
			replaced := false
			for i, old := range effects {
				if old.kind == e.kind {
					if e.frame >= old.frame {
						effects[i] = e
					}
					replaced = true
					break
				}
			}
			if !replaced {
				effects = append(effects, e)
			}
			a.effects[key] = effects
		}
	}
}

func (a alpine) effectAt(place string) (alpineEvent, bool) {
	best := alpineEvent{}
	score := -1
	for _, e := range a.effects[place] {
		if a.frame-e.frame >= e.life() {
			continue
		}
		rank := map[string]int{"storm": 5, "summit": 4, "fall": 3, "depart": 2, "work": 1}[e.kind]
		if rank > score || rank == score && e.frame > best.frame {
			best, score = e, rank
		}
	}
	return best, score >= 0
}

func (a *alpine) advance() { a.frame++ }

func alpineTick() tea.Cmd {
	return tea.Tick(alpineInterval, func(time.Time) tea.Msg { return alpineFrame{} })
}

type alpineFrame struct{}
