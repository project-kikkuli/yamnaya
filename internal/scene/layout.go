package scene

const (
	minPeak  = 6.0
	minRange = 10.0
	maxDepth = 3
)

// mount is one drawn mountain: a peak, or several peaks merged when the screen is crowded.
type mount struct {
	item
	cx, half float64
	lift     float64
	depth    int
	seed     uint64
}

// massif is the shoulder of a range that its peaks rise from.
type massif struct {
	id, name string
	x0, x1   float64
	depth    int
	weather  Weather
	selected bool
	seed     uint64
}

type layout struct {
	mounts  []mount
	massifs []massif
	depth   int
}

func plan(s Scene, width int) layout {
	var items []item
	for _, r := range s.Ranges {
		items = append(items, rangeItem(r, s.Selected))
	}
	for _, p := range s.Peaks {
		items = append(items, peakItem(p, s.Selected))
	}
	var l layout
	l.place(items, 0, float64(width), 0, 0)
	return l
}

func (l *layout) place(items []item, x0, x1 float64, peakDepth, rangeDepth int) {
	if len(items) == 0 || x1-x0 < 2 {
		return
	}
	slots := max(1, int((x1-x0)/minPeak))
	if len(items) > slots {
		var grouped []item
		for i := range slots {
			grouped = append(grouped, merge(items[i*len(items)/slots:(i+1)*len(items)/slots]))
		}
		items = grouped
	}
	total := 0.0
	for _, it := range items {
		total += it.weight()
	}
	x := x0
	for _, it := range items {
		w := (x1 - x0) * it.weight() / total
		switch {
		case it.isRange && w >= minRange:
			d := min(rangeDepth, maxDepth)
			l.depth = max(l.depth, d)
			l.massifs = append(l.massifs, massif{id: it.id, name: it.name, x0: x, x1: x + w, depth: d, weather: it.weather, selected: it.selected, seed: seedOf(it.id)})
			inset := w * .04
			l.place(it.children, x+inset, x+w-inset, d, rangeDepth+1)
		default:
			if it.isRange {
				it = merge([]item{it})
			}
			d := min(peakDepth, maxDepth)
			l.depth = max(l.depth, d)
			seed := seedOf(it.id)
			l.mounts = append(l.mounts, mount{item: it, cx: x + w/2 + (unit(seed, 3)-.5)*w*.2, half: w / 2, lift: .55 + .45*unit(seed, 1), depth: d, seed: seed})
		}
		x += w
	}
}
