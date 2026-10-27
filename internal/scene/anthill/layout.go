package anthill

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	minSlot   = 8.0
	minColony = 12.0
	maxDepth  = 3
)

// geo is where the cutaway's layers sit on a screen, in pixel rows: the surface, how deep a nest goes, how tall
// a full mound stands, and the first colony gallery under the nests.
type geo struct{ sy, nest, peak, g0 int }

func geometry(height int) geo {
	p := height * 2
	sy := (p * 40 / 100) &^ 1
	nest := max(6, p*17/100)
	return geo{sy: sy, nest: nest, peak: max(3, min(p*22/100, sy-10)), g0: (sy + nest + 4) &^ 1}
}

// gallery is the pixel row of the tunnel linking a colony depth levels inside a chief.
func (g geo) gallery(depth int) int { return g.g0 + 4*depth }

// mound is one drawn unit, or several merged on a crowded screen: a dome height pixels over the surface on
// cells x0..x1, a shaft down column cx to bottom, and its pull requests in chambers off the shaft.
type mound struct {
	item
	seed         uint64
	x0, x1, cx   int
	height       int
	parent       int
	bottom       int
	chambers     []chamber
	more, sealed int
}

// chamber is a pull request dug at pixel row y, on side -1 (west) or 1 (east) of the shaft.
type chamber struct {
	y, side int
	state   scene.CampState
	label   string
}

// colony is a group: a repository or a chief, its mounds on x0..x1 and its gallery under them from gx0 to gx1.
// A nested colony's gallery runs deeper and rises to its parent's at link.
type colony struct {
	id, name       string
	x0, x1, depth  int
	parent         int
	gx0, gx1, link int
	selected       bool
}

type layout struct {
	ms []mound
	cs []colony
}

func plan(s scene.Scene, width int, g geo) layout {
	var items []item
	for _, r := range s.Ranges {
		items = append(items, groupItem(r, s.Selected))
	}
	for _, p := range s.Peaks {
		items = append(items, unitItem(p, s.Selected))
	}
	var l layout
	l.place(items, 1, float64(width-1), -1, 0, g)
	l.galleries()
	return l
}

func (l *layout) place(items []item, x0, x1 float64, parent, depth int, g geo) {
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
		case it.isGroup && w >= minColony:
			k := len(l.cs)
			l.cs = append(l.cs, colony{id: it.id, name: it.name, x0: int(math.Round(x)), x1: int(math.Round(x + w)), depth: min(depth, maxDepth), parent: parent, selected: it.selected})
			inset := math.Max(1, w*.04)
			l.place(it.children, x+inset, x+w-inset, k, depth+1, g)
		default:
			if it.isGroup {
				it = merge([]item{it})
			}
			pd := -1
			if parent >= 0 {
				pd = l.cs[parent].depth
			}
			m := newMound(it, x, w, pd, g)
			m.parent = parent
			l.ms = append(l.ms, m)
		}
		x += w
	}
}

// galleries runs each colony's tunnel under its mounds and the links of the colonies nested in it, innermost
// first so a chief's gallery reaches every colony it holds.
func (l *layout) galleries() {
	for k := len(l.cs) - 1; k >= 0; k-- {
		c := &l.cs[k]
		lo, hi := math.MaxInt, math.MinInt
		for _, m := range l.ms {
			if m.parent == k {
				lo, hi = min(lo, m.cx), max(hi, m.cx)
			}
		}
		for _, sub := range l.cs[k+1:] {
			if sub.parent == k {
				lo, hi = min(lo, sub.link), max(hi, sub.link)
			}
		}
		if lo > hi {
			lo, hi = (c.x0+c.x1)/2, (c.x0+c.x1)/2
		}
		if hi-lo < 6 {
			mid := (lo + hi) / 2
			lo, hi = max(c.x0, mid-3), min(c.x1-1, mid+3)
		}
		c.gx0, c.gx1, c.link = lo, hi, (lo+hi)/2
	}
}

// newMound stands an item in the slot x..x+w: its progress raises the mound and spreads the nest under it.
func newMound(it item, x, w float64, depth int, g geo) mound {
	seed := scene.SeedOf(it.id)
	bw := int(clamp(math.Round(w*.75), 3, 18))
	if float64(bw) > w-1 {
		bw = max(2, int(w)-1)
	}
	jitter := (unit(seed, 3) - .5) * math.Max(0, w-float64(bw)-1) * .5
	x0 := int(math.Round(x + (w-float64(bw))/2 + jitter))
	m := mound{item: it, seed: seed, x0: x0, x1: x0 + bw}
	m.cx = (m.x0 + m.x1 - 1) / 2
	m.height = max(2, int(math.Round(math.Min(float64(g.peak), float64(bw)*1.5+2)*(.3+.7*m.grown()))))
	m.bottom = g.sy + g.nest
	if depth >= 0 {
		m.bottom = g.gallery(depth) + 1
	}
	m.dig(g)
	return m
}

// grown is how far the colony under the mound has grown: its progress, or all the way once done.
func (m *mound) grown() float64 {
	if m.weather == scene.Summit {
		return 1
	}
	return m.progress
}

// entry is the pixel row of the mound's top, where its shaft opens.
func (m *mound) entry(g geo) int { return g.sy - m.height }

// top is the pixel row of the mound's surface over column x when it stands height pixels tall.
func (m *mound) top(x int, height float64, g geo) int {
	half := float64(m.x1-m.x0) / 2
	dx := (float64(x) + .5 - float64(m.x0) - half) / half
	if math.Abs(dx) >= 1 {
		return g.sy
	}
	return g.sy - int(math.Round(height*math.Pow(1-dx*dx, .9)))
}

// nest is the brood comb under the mound, wider and deeper the more it has grown: its center and radii.
func (m *mound) nest(g geo) (cx, cy, rx, ry float64) {
	f := m.grown()
	rx = (float64(m.x1-m.x0)/2 + 1) * (.35 + .65*f)
	ry = float64(g.nest) * (.18 + .3*f)
	return float64(m.cx) + .5, float64(g.sy) + 1 + ry, rx, ry
}

// dig gives each pull request a chamber off the shaft, deeper the more gates it passed, east and west in turn;
// merged ones share one sealed chamber at the bottom.
func (m *mound) dig(g geo) {
	var camps []scene.Camp
	for _, c := range m.camps {
		if c.State == scene.Merged {
			m.sealed++
			if m.sealed > 1 {
				continue
			}
		}
		camps = append(camps, c)
	}
	slices.SortStableFunc(camps, func(a, c scene.Camp) int { return cmp.Compare(c.Stage, a.Stage) })
	limit := g.sy + g.nest - 1
	for k, c := range camps {
		depth := (g.sy + 3 + int(math.Round(float64(g.nest-5)*float64(min(max(c.Stage, 0), 6))/6))) &^ 1
		label := c.Label
		if c.State == scene.Merged && m.sealed > 1 {
			label = strconv.Itoa(m.sealed) + " sealed"
		}
		placed := false
		for _, side := range [2]int{1 - 2*(k%2), 2*(k%2) - 1} {
			for y := depth; y <= limit; y += 2 {
				if !m.dug(y, side) {
					m.chambers = append(m.chambers, chamber{y: y, side: side, state: c.State, label: label})
					placed = true
					break
				}
			}
			if placed {
				break
			}
		}
		if !placed {
			m.more++
		}
	}
}

func (m *mound) dug(y, side int) bool {
	for _, c := range m.chambers {
		if c.side == side && c.y == y {
			return true
		}
	}
	return false
}

func (m *mound) count(st scene.Stance) int {
	n := 0
	for _, c := range m.team {
		if c.Stance == st {
			n++
		}
	}
	return n
}
