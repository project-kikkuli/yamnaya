package skyline

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	minSlot     = 6.0
	minDistrict = 10.0
	maxDepth    = 3
	// headroom is the rows kept above the tallest building for its crane, beacon, marker and name.
	headroom = 7
)

// building is one drawn unit: a goal or session, or several merged when the screen is crowded. It stands on
// base, the pixel row under its ground floor, planned floors tall, of which built are finished.
type building struct {
	item
	seed           uint64
	x0, x1, depth  int
	base           int
	planned, built int
	style          int
	// floors holds, per floor, one more than the state of the pull request on it, or 0.
	floors []int8
	prs    []prFloor
	// more is how many pull requests found no floor of their own.
	more int
}

type prFloor struct {
	floor int
	state scene.CampState
	label string
}

// district is a group: a repository or a chief, its buildings standing inside x0..x1. Nested districts
// stand on a podium depth floors high.
type district struct {
	id, name      string
	x0, x1, depth int
	selected      bool
}

type layout struct {
	bs []building
	ds []district
}

// horizon is the pixel row of the street on a screen height cells tall.
func horizon(height int) int { return (height * 2 * 64 / 100) &^ 1 }

func plan(s scene.Scene, width, hz int) layout {
	var items []item
	for _, r := range s.Ranges {
		items = append(items, groupItem(r, s.Selected))
	}
	for _, p := range s.Peaks {
		items = append(items, unitItem(p, s.Selected))
	}
	var l layout
	l.place(items, 1, float64(width-1), 0, 0, hz)
	return l
}

func (l *layout) place(items []item, x0, x1 float64, unitDepth, groupDepth, hz int) {
	if len(items) == 0 || x1-x0 < 2 {
		return
	}
	slots := max(1, int((x1-x0)/minSlot))
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
		case it.isGroup && w >= minDistrict:
			d := min(groupDepth, maxDepth)
			l.ds = append(l.ds, district{id: it.id, name: it.name, x0: int(math.Round(x)), x1: int(math.Round(x + w)), depth: d, selected: it.selected})
			inset := math.Max(1, w*.04)
			l.place(it.children, x+inset, x+w-inset, d, groupDepth+1, hz)
		default:
			if it.isGroup {
				it = merge([]item{it})
			}
			l.bs = append(l.bs, newBuilding(it, x, w, min(unitDepth, maxDepth), hz))
		}
		x += w
	}
}

// newBuilding stands an item in the slot x..x+w: more agents and pull requests plan it taller, and its
// progress is how much of that is built.
func newBuilding(it item, x, w float64, depth, hz int) building {
	seed := scene.SeedOf(it.id)
	bw := int(clamp(math.Round(w*.62), 3, 14))
	if float64(bw) > w-1 {
		bw = max(2, int(w)-1)
	}
	jitter := (unit(seed, 3) - .5) * math.Max(0, w-float64(bw)-1) * .5
	x0 := int(math.Round(x + (w-float64(bw))/2 + jitter))
	b := building{item: it, seed: seed, x0: x0, x1: x0 + bw, depth: depth, base: hz - 2*depth, style: int(spread(seed, 4) % 3)}
	room := max(3, b.base/2-headroom)
	work := float64(len(it.team) + len(it.camps))
	lift := .45 + .4*math.Min(1, math.Log1p(work)/math.Log(25)) + .15*unit(seed, 1)
	b.planned = max(2, int(math.Round(float64(room)*lift)))
	b.built = b.planned
	if it.weather != scene.Summit {
		b.built = max(1, min(b.planned, int(math.Round(float64(b.planned)*(.12+.88*it.progress)))))
	}
	b.stack()
	return b
}

// stack gives each pull request a floor at the height of the gates it passed; merged ones share one.
func (b *building) stack() {
	b.floors = make([]int8, b.planned)
	var camps []scene.Camp
	merged := 0
	for _, c := range b.camps {
		if c.State == scene.Merged {
			merged++
			if merged > 1 {
				continue
			}
		}
		camps = append(camps, c)
	}
	slices.SortStableFunc(camps, func(a, c scene.Camp) int { return cmp.Compare(c.Stage, a.Stage) })
	for k, c := range camps {
		f := int(math.Round(float64(b.planned-1) * float64(min(max(c.Stage, 0), 6)) / 6))
		for f >= 0 && b.floors[f] != 0 {
			f--
		}
		if f < 0 {
			b.more = len(camps) - k
			return
		}
		label := c.Label
		if c.State == scene.Merged && merged > 1 {
			label = strconv.Itoa(merged) + " merged"
		}
		b.floors[f] = int8(c.State) + 1
		b.prs = append(b.prs, prFloor{floor: f, state: c.State, label: label})
	}
}

func (b *building) cx() int { return (b.x0 + b.x1 - 1) / 2 }

func (b *building) busy() int {
	n := 0
	for _, c := range b.team {
		if c.Stance == scene.Climbing {
			n++
		}
	}
	return n
}

// crane reports whether a tower crane works the roof: while anyone is busy and the building still stands.
func (b *building) crane() bool {
	return b.busy() > 0 && b.dying == 0 && b.weather != scene.Eruption && b.weather != scene.Summit
}

// drop is how far a building being demolished has sunk into its own dust, in pixels.
func (b *building) drop() int {
	if b.dying == 0 {
		return 0
	}
	return int(math.Round(float64(2*b.planned) * smooth(.12, .92, b.dying)))
}
