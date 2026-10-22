package scene

import "math"

// item is a peak or range in the shape layout works with; a crowded stretch becomes one merged peak.
type item struct {
	id, name string
	weather  Weather
	climbers int
	progress float64
	selected bool
	isRange  bool
	children []item
	leaves   int
}

func peakItem(p Peak, sel string) item {
	return item{id: p.ID, name: p.Name, weather: p.Weather, climbers: p.Climbers, progress: clamp(p.Progress, 0, 1), selected: p.ID == sel, leaves: 1}
}

func rangeItem(r Range, sel string) item {
	it := item{id: r.ID, name: r.Name, weather: r.weather(), isRange: true, selected: r.ID == sel}
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

func (it item) weight() float64 { return math.Pow(float64(it.leaves), .75) }

// holdsSelection reports whether the selected peak or range is this item or inside it.
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

// merge folds items into one peak carrying their climbers, weather and selection.
func merge(items []item) item {
	if len(items) == 1 && !items[0].isRange {
		return items[0]
	}
	out := item{id: items[0].id + "+", weather: Clouds}
	var parts []Weather
	climbing := 0.0
	for _, it := range items {
		parts = append(parts, it.weather)
		out.leaves += it.leaves
		out.selected = out.selected || it.holdsSelection()
		var walk func(item)
		walk = func(c item) {
			if !c.isRange {
				out.climbers += c.climbers
				out.progress += c.progress * float64(c.climbers)
				climbing += float64(c.climbers)
			}
			for _, k := range c.children {
				walk(k)
			}
		}
		walk(it)
	}
	if climbing > 0 {
		out.progress /= climbing
	}
	if len(items) == 1 {
		out.name = items[0].name
	}
	out.weather = combine(Clouds, parts)
	return out
}
