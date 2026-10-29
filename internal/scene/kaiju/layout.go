package kaiju

import (
	"math"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// minSlot is the narrowest stretch of shore, in pixels, a monster wades in on; two lanes share it, staggered.
const minSlot = 7.0

// item is a unit or group in the shape layout works with; a crowded stretch becomes one merged monster.
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
	children []item
	leaves   int
}

func peakItem(p scene.Peak, sel string) item {
	progress := p.Progress
	if math.IsNaN(progress) {
		progress = 0
	}
	return item{id: p.ID, name: ansi.Strip(p.Name), weather: p.Weather, team: p.Team, camps: p.Camps, needsYou: p.NeedsYou,
		progress: clamp(progress, 0, 1), dying: clamp(p.Dying, 0, 1), selected: p.ID == sel, leaves: 1}
}

func rangeItem(r scene.Range, sel string) item {
	it := item{id: r.ID, name: ansi.Strip(r.Name), weather: r.Sky(), isGroup: true, selected: r.ID == sel}
	for _, p := range r.Peaks {
		it.children = append(it.children, peakItem(p, sel))
	}
	for _, sub := range r.Ranges {
		it.children = append(it.children, rangeItem(sub, sel))
	}
	for _, c := range it.children {
		it.leaves += c.leaves
	}
	it.leaves = max(1, it.leaves)
	return it
}

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

// merge folds items into one monster carrying their agents, pull requests, alerts, weather and selection.
func merge(items []item) item {
	if len(items) == 1 && !items[0].isGroup {
		return items[0]
	}
	out := item{id: items[0].id}
	var parts []scene.Weather
	units := 0.0
	var walk func(item)
	walk = func(c item) {
		if !c.isGroup {
			out.team = append(out.team, c.team...)
			out.camps = append(out.camps, c.camps...)
			out.needsYou = out.needsYou || c.needsYou && c.dying == 0
			out.progress += c.progress
			units++
		}
		for _, k := range c.children {
			walk(k)
		}
	}
	for _, it := range items {
		parts = append(parts, it.weather)
		out.leaves += it.leaves
		out.selected = out.selected || it.holdsSelection()
		walk(it)
	}
	if units > 0 {
		out.progress /= units
	}
	if units == 1 && len(items) == 1 && len(items[0].children) == 1 {
		out.name = items[0].children[0].name
	}
	out.weather = scene.Combine(scene.Clouds, parts)
	return out
}

func fold(items []item, slots int) []item {
	out := make([]item, 0, slots)
	for i := range slots {
		out = append(out, merge(items[i*len(items)/slots:(i+1)*len(items)/slots]))
	}
	return out
}

// monster is one drawn unit, in pixels: a unit, or several merged when the shore is crowded.
type monster struct {
	item
	seed uint64
	// x is its middle; foot is where it stands at its progress, water where the bay cuts it off, or below foot
	// once it is ashore.
	x, foot, water float64
	// base is where a summit's ruined tower stands, the monster on top of it; foot elsewhere.
	base float64
	w, h float64
	lane int
	kind *kind
	// young is drawn for a moth still a larva, crawling.
	young bool
	// facing is 1 when it looks east, -1 west.
	facing float64
}

func (m *monster) top() float64 { return m.foot - m.h }

// district is a group's stretch of the city; a chief's nested groups are districts within it.
type district struct {
	id, name string
	x0, x1   float64
	depth    int
	selected bool
	seed     uint64
}

// layout is the frame of the picture in pixels: the letterbox's inner edges, the ground line the city stands on,
// the quay's edge where the bay begins; and the monsters and districts across it.
type layout struct {
	top, bottom, ground, quay float64
	monsters                  []monster
	districts                 []district
	unitNames, agentNames     []string
	groupNames                []string
}

// bars is how many cell rows the letterbox takes at the top and the bottom of a screen this tall.
func bars(rows int) (int, int) {
	if rows >= 30 {
		return 1, 2
	}
	return 1, 1
}

func plan(s scene.Scene, width, rows int) layout {
	tb, bb := bars(rows)
	l := layout{top: float64(2 * tb), bottom: float64(2 * (rows - bb))}
	h := l.bottom - l.top
	l.ground = l.top + .5*h
	l.quay = l.ground + .06*h
	s.Walk(func(p scene.Peak) {
		l.unitNames = append(l.unitNames, ansi.Strip(p.Name))
		for _, c := range p.Team {
			l.agentNames = append(l.agentNames, ansi.Strip(c.Name))
		}
	}, func(r scene.Range) { l.groupNames = append(l.groupNames, ansi.Strip(r.Name)) })
	var items []item
	for _, r := range s.Ranges {
		items = append(items, rangeItem(r, s.Selected))
	}
	for _, p := range s.Peaks {
		items = append(items, peakItem(p, s.Selected))
	}
	if len(items) == 0 || h < 8 {
		return l
	}
	l.spread(items, 0, float64(width), max(1, int(float64(width)/minSlot)), 0)
	l.wade(h)
	return l
}

// spread lays items side by side from x0 to x1, each as wide as its share of the units, folding them into slots
// monsters at most.
func (l *layout) spread(items []item, x0, x1 float64, slots, depth int) {
	leaves := 0
	for _, it := range items {
		leaves += it.leaves
	}
	if leaves > slots && len(items) > slots {
		items = fold(items, slots)
		leaves = 0
		for _, it := range items {
			leaves += it.leaves
		}
	}
	x := x0
	for _, it := range items {
		w := (x1 - x0) * float64(it.leaves) / float64(leaves)
		share := max(1, int(math.Round(float64(slots)*float64(it.leaves)/float64(leaves))))
		if it.isGroup && len(it.children) > 0 && share > 1 {
			l.districts = append(l.districts, district{id: it.id, name: it.name, x0: x, x1: x + w, depth: depth,
				selected: it.selected, seed: scene.SeedOf(it.id)})
			l.spread(it.children, x, x+w, share, depth+1)
		} else {
			if it.isGroup {
				l.districts = append(l.districts, district{id: it.id, name: it.name, x0: x, x1: x + w, depth: depth,
					selected: it.selected, seed: scene.SeedOf(it.id)})
				it = merge([]item{it})
			}
			seed := scene.SeedOf(it.id)
			l.monsters = append(l.monsters, monster{item: it, seed: seed, x: x + w/2, w: w,
				kind: roster[scene.Spread(seed, 1)%uint64(len(roster))], facing: float64(1 - 2*int(unit(seed, 2)*2))})
		}
		x += w
	}
}

// wade sizes each monster to its stretch of shore and sets it where its progress has brought it: out in the bay
// up to its chest at the start, striding up the quay into the city when done. Neighbors take turns on two lanes
// when the shore is crowded, the front lane lower on the screen.
func (l *layout) wade(h float64) {
	crowded := false
	for _, m := range l.monsters {
		crowded = crowded || m.w < 2*minSlot
	}
	for i := range l.monsters {
		m := &l.monsters[i]
		span := m.w
		if crowded && len(l.monsters) > 1 {
			m.lane = i % 2
			span *= 1.7
		} else {
			span *= .8
		}
		work := float64(len(m.team) + len(m.camps))
		size := math.Max(8, math.Min(.52*h, span*1.25)*(.86+.14*math.Min(1, math.Log1p(work)/math.Log(12))))
		m.young = m.kind.young != nil && m.progress < .4
		sp := m.sprite(0)
		k := size / math.Max(float64(sp.w)*.8, float64(sp.h))
		m.h, m.w = float64(sp.h)*k, float64(sp.w)*k
		lane := float64(m.lane)
		end := l.ground + .01*h + lane*.07*h
		start := l.bottom - .05*h + lane*.05*h
		m.foot = lerp(start, end, m.progress)
		m.water = m.foot + 1
		if m.foot > l.quay {
			m.water = m.foot - .62*m.h*(m.foot-l.quay)/(start-l.quay)
		}
		m.base = m.foot
		if m.kind.fly > 0 && !m.young {
			m.foot -= m.kind.fly * m.h
			m.water = m.foot + 1
		}
		if m.weather == scene.Summit {
			m.base = math.Min(m.foot, l.quay)
			m.foot = m.base - .3*m.h
			m.water = m.foot + 1
		}
	}
}

// group is the innermost district at pixel column x.
func (l *layout) group(x float64) int {
	found := -1
	for i, d := range l.districts {
		if x >= d.x0 && x < d.x1 && (found < 0 || d.depth >= l.districts[found].depth) {
			found = i
		}
	}
	return found
}
