package pyramids

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	minSlot       = 6.0
	minNecropolis = 12.0
	maxDepth      = 3
)

// headroom is the pixels kept above the tallest pyramid for its capstone, beacon, marker and name.
func headroom(hz int) int { return max(6, hz*18/100) }

// pyramid is one drawn unit: a goal or session, or several merged when the screen is crowded. Its base spans
// cells x0..x1 on pixel row base, and it rises planned courses, a pixel each, of which built are laid.
type pyramid struct {
	item
	seed          uint64
	x0, x1, depth int
	base          int
	planned       int
	built         int
	// cased marks, per course, one whose stones a merged pull request placed.
	cased  []bool
	blocks []block
	// more is how many pull requests found no row of their own on the ramp.
	more int
}

// block is a pull request on the east ramp at the cell row of the gates it passed.
type block struct {
	row   int
	state scene.CampState
	label string
}

// necropolis is a group: a repository or a chief, its pyramids standing inside x0..x1. Nested ones stand on
// a plateau depth steps high; a chief's is linked by a causeway.
type necropolis struct {
	id, name      string
	x0, x1, depth int
	selected      bool
	chief         bool
}

type layout struct {
	ps []pyramid
	ns []necropolis
	// order is the pyramids back to front: the tallest first, so a smaller one stands in front of it.
	order []int
}

// horizon is the pixel row where the desert meets the sky on a screen height cells tall.
func horizon(height int) int { return (height * 2 * 62 / 100) &^ 1 }

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
	l.order = make([]int, len(l.ps))
	for i := range l.order {
		l.order[i] = i
	}
	slices.SortStableFunc(l.order, func(a, b int) int { return cmp.Compare(l.ps[b].planned, l.ps[a].planned) })
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
		case it.isGroup && w >= minNecropolis:
			d := min(groupDepth, maxDepth)
			l.ns = append(l.ns, necropolis{id: it.id, name: it.name, x0: int(math.Round(x)), x1: int(math.Round(x + w)), depth: d, selected: it.selected, chief: it.chief})
			inset := math.Max(1, w*.04)
			l.place(it.children, x+inset, x+w-inset, d, groupDepth+1, hz)
		default:
			if it.isGroup {
				it = merge([]item{it})
			}
			l.ps = append(l.ps, newPyramid(it, x, w, min(unitDepth, maxDepth), hz))
		}
		x += w
	}
}

// newPyramid stands an item in the slot x..x+w: more agents and pull requests raise it taller, as wide as the
// classic slope wants when there is room, steeper and overlapping its neighbours when the slots are narrow, with
// an odd base so the apex is one stone. Its progress is how many of the courses are laid.
func newPyramid(it item, x, w float64, depth, hz int) pyramid {
	seed := scene.SeedOf(it.id)
	work := float64(len(it.team) + len(it.camps))
	room := max(4, hz-2*depth-headroom(hz))
	planned := max(4, int(math.Round(float64(room)*(.5+.35*math.Min(1, math.Log1p(work)/math.Log(25))+.15*unit(seed, 1)))))
	widest := w * lerp(2.2, 1.15, smooth(8, 30, w))
	bw := int(clamp(math.Round(float64(planned)/.62), 5, math.Max(5, widest))) | 1
	jitter := (unit(seed, 3) - .5) * w * .2
	x0 := int(math.Round(x + (w-float64(bw))/2 + jitter))
	b := pyramid{item: it, seed: seed, x0: x0, x1: x0 + bw, depth: depth, base: hz - 2*depth}
	b.planned = min(planned, int(float64(bw)*1.6))
	b.built = b.planned
	if it.weather != scene.Summit {
		b.built = max(1, min(b.planned, int(math.Round(float64(b.planned)*(.12+.88*it.progress)))))
	}
	b.stack()
	return b
}

// stack sets each pull request on the ramp at the height of the gates it passed, the furthest first and the
// rest below when a row is taken; merged ones share one block, placed in the course at its row.
func (b *pyramid) stack() {
	b.cased = make([]bool, b.planned)
	top, bottom := b.apexRow(), b.groundRow()-1
	if bottom < top {
		return
	}
	taken := make([]bool, bottom-top+1)
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
		row := bottom - int(math.Round(float64(bottom-top)*float64(min(max(c.Stage, 0), 6))/6))
		for row <= bottom && taken[row-top] {
			row++
		}
		if row > bottom {
			b.more = len(camps) - k
			return
		}
		taken[row-top] = true
		label := c.Label
		if c.State == scene.Merged {
			if merged > 1 {
				label = strconv.Itoa(merged) + " merged"
			}
			for _, k := range [2]int{b.course(row), b.course(row) + 1} {
				if k >= 0 && k < b.built {
					b.cased[k] = true
				}
			}
		}
		b.blocks = append(b.blocks, block{row: row, state: c.State, label: label})
	}
}

func (b *pyramid) cx() float64 { return float64(b.x0+b.x1) / 2 }

func (b *pyramid) mid() int { return (b.x0 + b.x1 - 1) / 2 }

// span is the cells course k covers, west to east, narrowing to the one apex stone.
func (b *pyramid) span(k int) (int, int) {
	h := float64(b.x1-b.x0)/2*(1-(float64(k)+.5)/float64(b.planned)) + .35
	return int(math.Ceil(b.cx() - h - .5)), int(math.Floor(b.cx() + h - .5))
}

// course is the course in the lower pixel of cell row row; below zero under the base.
func (b *pyramid) course(row int) int { return b.base - 2 - 2*row }

// edges is the west and east face cells at cell row row, the course clamped onto the pyramid.
func (b *pyramid) edges(row int) (int, int) {
	return b.span(min(max(b.course(row), 0), b.planned-1))
}

func (b *pyramid) apexRow() int { return (b.base - b.planned) / 2 }

// crestRow is the highest cell row the pyramid reaches: its apex, or what a collapse left standing.
func (b *pyramid) crestRow() int {
	if b.weather == scene.Eruption {
		return (b.base - b.collapse()) / 2
	}
	return b.apexRow()
}

// collapse is how many courses still stand after the top fell in.
func (b *pyramid) collapse() int  { return max(1, b.built*55/100) }
func (b *pyramid) topRow() int    { return (b.base - b.built) / 2 }
func (b *pyramid) groundRow() int { return b.base / 2 }

func (b *pyramid) busy() int {
	n := 0
	for _, c := range b.team {
		if c.Stance == scene.Climbing {
			n++
		}
	}
	return n
}

// out is how far the ramp juts from the face at course k: a wedge of mud brick widest at the foot, one
// cell above the courses laid.
func (b *pyramid) out(k int) int {
	if k >= b.built {
		return 1
	}
	return 1 + int(math.Round(float64(b.built-1-k)*.3))
}

// westAt is the cell on the west ramp's surface at cell row row, where its workers stand.
func (b *pyramid) westAt(row int) int {
	k := min(max(b.course(row), 0), b.planned-1)
	lo, _ := b.span(k)
	return lo - b.out(k)
}

// eastAt is the cell on the east ramp's surface at cell row row, where its blocks wait.
func (b *pyramid) eastAt(row int) int {
	k := min(max(b.course(row), 0), b.planned-1)
	_, hi := b.span(k)
	return hi + b.out(k)
}

// ramps reports whether the mud-brick ramps still stand: while the pyramid rises, not once it is capped,
// fallen or being killed.
func (b *pyramid) ramps() bool {
	return b.dying == 0 && b.weather != scene.Summit && b.weather != scene.Eruption
}

// hauling reports whether the east ramp carries blocks still to be placed.
func (b *pyramid) hauling() bool {
	if !b.ramps() {
		return false
	}
	for _, k := range b.blocks {
		if k.state != scene.Merged {
			return true
		}
	}
	return false
}
