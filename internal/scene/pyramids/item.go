package pyramids

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// item is a unit or district in the shape layout works with; a crowded stretch becomes one merged pyramid.
type item struct {
	id, name string
	weather  scene.Weather
	team     []scene.Climber
	camps    []scene.Camp
	needsYou bool
	progress float64
	dying    float64
	selected bool
	isGroup  bool
	// chief is a group holding nested groups: a royal complex its causeway links.
	chief    bool
	children []item
	leaves   int
}

func unitItem(p scene.Peak, sel string) item {
	return item{id: p.ID, name: p.Name, weather: p.Weather, team: p.Team, camps: p.Camps, needsYou: p.NeedsYou, progress: progressOf(p.Progress), dying: clamp(p.Dying, 0, 1), selected: p.ID == sel, leaves: 1}
}

func groupItem(r scene.Range, sel string) item {
	it := item{id: r.ID, name: r.Name, weather: r.Sky(), isGroup: true, chief: len(r.Ranges) > 0, selected: r.ID == sel}
	for _, p := range r.Peaks {
		it.children = append(it.children, unitItem(p, sel))
	}
	for _, sub := range r.Ranges {
		it.children = append(it.children, groupItem(sub, sel))
	}
	for _, c := range it.children {
		it.leaves += c.leaves
	}
	it.leaves = max(1, it.leaves)
	return it
}

func (it item) weight() float64 { return math.Pow(float64(it.leaves), .75) }

func (it item) holdsSelection() bool {
	if it.selected {
		return true
	}
	for _, c := range it.children {
		if c.holdsSelection() {
			return true
		}
	}
	return false
}

// merge folds items into one pyramid carrying their agents, pull requests, weather and selection.
func merge(items []item) item {
	if len(items) == 1 && !items[0].isGroup {
		return items[0]
	}
	out := item{id: items[0].id + "+"}
	var parts []scene.Weather
	units := 0.0
	for _, it := range items {
		parts = append(parts, it.weather)
		out.leaves += it.leaves
		out.selected = out.selected || it.holdsSelection()
		var walk func(item)
		walk = func(c item) {
			if !c.isGroup {
				out.team = append(out.team, c.team...)
				out.camps = append(out.camps, c.camps...)
				out.needsYou = out.needsYou || c.needsYou
				out.progress += c.progress
				units++
			}
			for _, k := range c.children {
				walk(k)
			}
		}
		walk(it)
	}
	if units > 0 {
		out.progress /= units
	}
	if len(items) == 1 {
		out.name = items[0].name
	}
	out.weather = scene.Combine(scene.Clouds, parts)
	return out
}

func progressOf(f float64) float64 {
	if math.IsNaN(f) {
		return 0
	}
	return clamp(f, 0, 1)
}
