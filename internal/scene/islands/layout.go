package islands

import (
	"math"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Sizes are in pixels, two to a cell row and one to a column, so a pixel is about square.
const (
	minSlot  = 10.0
	minGroup = 14.0
)

// item is a unit or group in the shape layout works with; a crowded stretch becomes one merged island.
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
	chief    bool
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
	it := item{id: r.ID, name: ansi.Strip(r.Name), weather: r.Sky(), isGroup: true, chief: !strings.HasPrefix(r.ID, "repo:"), selected: r.ID == sel}
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

// merge folds items into one island carrying their boats, buildings, alerts, weather and selection.
func merge(items []item) item {
	if len(items) == 1 && !items[0].isGroup {
		return items[0]
	}
	out := item{id: items[0].id}
	if len(items) == 1 {
		out.name = items[0].name
	}
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
				out.needsYou = out.needsYou || c.needsYou && c.dying == 0
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
	out.weather = scene.Combine(scene.Clouds, parts)
	return out
}

type rect struct{ x0, y0, x1, y1 float64 }

func (r rect) w() float64              { return r.x1 - r.x0 }
func (r rect) h() float64              { return r.y1 - r.y0 }
func (r rect) mid() (float64, float64) { return (r.x0 + r.x1) / 2, (r.y0 + r.y1) / 2 }
func (r rect) has(x, y float64) bool   { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }
func around(x, y, half float64) rect   { return rect{x - half, y - half, x + half, y + half} }
func (r rect) inset(dx, top, bottom float64) rect {
	return rect{r.x0 + dx, r.y0 + top, r.x1 - dx, r.y1 - bottom}
}

// isle is one drawn island, in pixels: a unit, or several merged when the screen is crowded.
type isle struct {
	item
	cx, cy, r float64
	seed      uint64
}

// group is an archipelago's area; a chief's holds a lagoon its islands ring.
type group struct {
	id, name string
	area     rect
	chief    bool
	selected bool
	// lagoon is the ellipse at lx, ly with radii la, lb; la is zero for none.
	lx, ly, la, lb float64
	seed           uint64
}

type layout struct {
	isles  []isle
	groups []group
}

// plan lays the board out as archipelagos side by side across the sea between top and bottom, in pixels.
func plan(s scene.Scene, width int, top, bottom float64) layout {
	var items []item
	for _, r := range s.Ranges {
		items = append(items, rangeItem(r, s.Selected))
	}
	for _, p := range s.Peaks {
		items = append(items, peakItem(p, s.Selected))
	}
	var l layout
	if len(items) == 0 || bottom-top < 4 {
		return l
	}
	area := rect{0, top, float64(width), bottom}
	if slots := max(1, int(area.w()/minSlot)); len(items) > slots {
		items = fold(items, slots)
	}
	total := 0.0
	for _, it := range items {
		total += it.weight()
	}
	x := area.x0
	for _, it := range items {
		w := area.w() * it.weight() / total
		l.place(it, rect{x, area.y0, x + w, area.y1})
		x += w
	}
	return l
}

func fold(items []item, slots int) []item {
	out := make([]item, 0, slots)
	for i := range slots {
		out = append(out, merge(items[i*len(items)/slots:(i+1)*len(items)/slots]))
	}
	return out
}

func (l *layout) place(it item, area rect) {
	if it.isGroup && len(it.children) > 0 && area.w() >= minGroup && area.h() >= minSlot*1.3 {
		l.group(it, area)
		return
	}
	if it.isGroup {
		it = merge([]item{it})
	}
	l.isle(it, area)
}

func (l *layout) group(it item, area rect) {
	gi := len(l.groups)
	l.groups = append(l.groups, group{id: it.id, name: it.name, area: area, chief: it.chief, selected: it.selected, seed: scene.SeedOf(it.id)})
	inner := area.inset(1, 0, 2)
	if it.chief && len(it.children) >= 3 && l.ring(gi, it.children, inner) {
		return
	}
	if it.chief {
		g := &l.groups[gi]
		g.lx, g.ly = inner.mid()
		g.la, g.lb = inner.w()*.4, inner.h()*.36
	}
	l.grid(it.children, inner)
}

// ring sets a chief's members round a lagoon, as an atoll's islands stand round theirs.
func (l *layout) ring(gi int, kids []item, area rect) bool {
	cx, cy := area.mid()
	s0 := math.Min(area.w(), area.h()) * .36
	a, b := area.w()/2-s0/2, area.h()/2-s0/2
	per := 2 * math.Pi * math.Sqrt((a*a+b*b)/2)
	slots := int(per / minSlot)
	if slots < 3 || s0 < minSlot {
		return false
	}
	if len(kids) > slots {
		kids = fold(kids, slots)
	}
	n := float64(len(kids))
	size := math.Min(s0, per/n*.92)
	g := &l.groups[gi]
	g.lx, g.ly, g.la, g.lb = cx, cy, a*.82, b*.82
	for k, c := range kids {
		th := -math.Pi/2 + 2*math.Pi*float64(k)/n
		l.place(c, around(cx+a*math.Cos(th), cy+b*math.Sin(th), size/2))
	}
	return true
}

// grid spreads an archipelago's members over rows of cells as near square as fit.
func (l *layout) grid(kids []item, area rect) {
	if len(kids) == 0 {
		return
	}
	if fit := max(1, int(area.w()/minSlot)) * max(1, int(area.h()/minSlot)); len(kids) > fit {
		kids = fold(kids, fit)
	}
	n := len(kids)
	best, rows := math.Inf(1), 1
	for r := 1; r <= n; r++ {
		c := (n + r - 1) / r
		cw, ch := area.w()/float64(c), area.h()/float64(r)
		if (cw < minSlot || ch < minSlot) && r > 1 {
			continue
		}
		if score := math.Abs(math.Log(cw / ch)); score < best {
			best, rows = score, r
		}
	}
	cols := (n + rows - 1) / rows
	cw, ch := area.w()/float64(cols), area.h()/float64(rows)
	for k, c := range kids {
		r, col := k/cols, k%cols
		inRow := min(cols, n-r*cols)
		x0 := area.x0 + (area.w()-float64(inRow)*cw)/2 + float64(col)*cw
		y0 := area.y0 + float64(r)*ch
		l.place(c, rect{x0, y0, x0 + cw, y0 + ch})
	}
}

// isle sets an island in its cell, bigger the more agents and pull requests it has, nudged off center by its seed.
func (l *layout) isle(it item, area rect) {
	seed := scene.SeedOf(it.id)
	slot := math.Min(area.w(), area.h()) / 2
	work := float64(len(it.team) + len(it.camps))
	r := slot * (.46 + .28*math.Min(1, math.Log1p(work)/math.Log(16))) * (.92 + .16*unit(seed, 1))
	r = math.Max(2, r)
	room := math.Max(0, slot-r*1.3)
	cx, cy := area.mid()
	cx += (unit(seed, 2) - .5) * room
	cy += (unit(seed, 3)-.5)*room*.6 + 1
	l.isles = append(l.isles, isle{item: it, cx: cx, cy: cy, r: r, seed: seed})
}
