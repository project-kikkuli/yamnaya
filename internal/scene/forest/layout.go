package forest

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	minSlot  = 6.0
	minGrove = 10.0
	maxDepth = 3
	// headroom is the rows kept above the tallest tree for its bloom, lantern, marker and name.
	headroom = 6
)

// Tree styles, picked by seed: they mean nothing.
const (
	broadleaf = iota
	conifer
	birch
)

// tree is one drawn unit: a goal or session, or several merged when the screen is crowded. Its trunk stands
// on base, the pixel row under its roots, in column cx; it grows to planned pixels and stands h tall now.
type tree struct {
	item
	seed          uint64
	x0, x1, depth int
	cx, base      int
	planned, h    int
	// tw is how many pixels the trunk reaches either side of cx; rx and ry are the crown's radii, vc its height.
	tw         int
	rx, ry, vc float64
	style      int
	nests      []nest
	// more is how many pull requests found no nest of their own.
	more int
}

// nest is a pull request on the trunk: its cell row, at the height of the gates it passed.
type nest struct {
	row   int
	state scene.CampState
	label string
}

// grove is a group: a repository's grove, or a chief's clearing. Nested clearings rise on a knoll depth
// cells high.
type grove struct {
	id, name      string
	x0, x1, depth int
	chief         bool
	selected      bool
	seed          uint64
}

type layout struct {
	ts []tree
	gs []grove
}

// horizon is the pixel row of the forest floor on a screen height cells tall.
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
		case it.isGroup && w >= minGrove:
			d := min(groupDepth, maxDepth)
			l.gs = append(l.gs, grove{id: it.id, name: it.name, x0: int(math.Round(x)), x1: int(math.Round(x + w)), depth: d, chief: it.chief, selected: it.selected, seed: scene.SeedOf(it.id)})
			inset := math.Max(1, w*.05)
			if it.chief {
				inset = math.Max(3, w*.08)
			}
			l.place(it.children, x+inset, x+w-inset, d, groupDepth+1, hz)
		default:
			if it.isGroup {
				it = merge([]item{it})
			}
			l.ts = append(l.ts, newTree(it, x, w, min(unitDepth, maxDepth), hz))
		}
		x += w
	}
}

// newTree plants an item in the slot x..x+w: more agents and pull requests let it grow taller, and its
// progress is how much of that it has grown.
func newTree(it item, x, w float64, depth, hz int) tree {
	seed := scene.SeedOf(it.id)
	t := tree{item: it, seed: seed, depth: depth, base: hz - 2*depth, style: int(spread(seed, 4) % 3)}
	t.x0, t.x1 = int(math.Round(x)), int(math.Round(x+w))
	jitter := (unit(seed, 3) - .5) * math.Max(0, w-6) * .3
	t.cx = int(math.Round(x + w/2 + jitter))
	room := max(8, t.base-2*headroom)
	work := float64(len(it.team) + len(it.camps))
	lift := .45 + .4*math.Min(1, math.Log1p(work)/math.Log(25)) + .15*unit(seed, 1)
	t.planned = max(6, int(math.Round(float64(room)*lift)))
	t.h = t.planned
	if it.weather != scene.Summit {
		t.h = max(4, min(t.planned, int(math.Round(float64(t.planned)*(.15+.85*it.progress)))))
	}
	h := float64(t.h)
	t.rx = clamp(math.Min(w*.46-.5, h*.42), 1.5, 12)
	t.tw = 0
	if t.rx >= 5 && t.h >= 16 {
		t.tw = 1
	}
	switch t.style {
	case conifer:
		t.vc, t.ry = h*.6, h*.42
	case birch:
		t.rx = math.Max(1.5, t.rx*.75)
		t.vc, t.ry = h*.66, h*.36
	default:
		t.vc, t.ry = h*.64, h*.38
	}
	t.stack()
	return t
}

// top is the cell row of the tree's crown tip.
func (t *tree) top() int { return (t.base - t.h) / 2 }

// ground is the cell row the roots stand in.
func (t *tree) ground() int { return (t.base - 1) / 2 }

// stack hangs each pull request's nest up the trunk at the height of the gates it passed; merged ones share
// one blossom. Two nests never share a row: a later one moves down.
func (t *tree) stack() {
	var camps []scene.Camp
	merged := 0
	for _, c := range t.camps {
		if c.State == scene.Merged {
			merged++
			if merged > 1 {
				continue
			}
		}
		camps = append(camps, c)
	}
	slices.SortStableFunc(camps, func(a, c scene.Camp) int { return cmp.Compare(c.Stage, a.Stage) })
	top, ground := t.top()+1, t.ground()
	used := map[int]bool{}
	for k, c := range camps {
		v := float64(t.h-2) * float64(min(max(c.Stage, 0), 6)) / 6
		row := min(ground, max(top, (t.base-1-int(math.Round(v)))/2))
		for row <= ground && used[row] {
			row++
		}
		if row > ground {
			t.more = len(camps) - k
			return
		}
		used[row] = true
		label := c.Label
		if c.State == scene.Merged && merged > 1 {
			label = strconv.Itoa(merged) + " merged"
		}
		t.nests = append(t.nests, nest{row: row, state: c.State, label: label})
	}
}

// moving reports whether the tree is drawn fresh each frame rather than in the backdrop: it sways in a storm,
// burns, or is being felled.
func (t *tree) moving() bool {
	return t.dying > 0 || t.weather == scene.Storm || t.weather == scene.Eruption
}

// fell is how far a felled tree has turned toward the ground, in radians, and which way it falls.
func (t *tree) fell() (float64, float64) {
	dir := 1.0
	if t.seed%2 == 0 {
		dir = -1
	}
	k := clamp((t.dying-.08)/.62, 0, 1)
	return math.Pi / 2 * .94 * k * k, dir
}
