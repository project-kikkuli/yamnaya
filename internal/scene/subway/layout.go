package subway

import (
	"math"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// line is a unit drawn as a transit line; a line folding several units on a crowded map carries all their work.
type line struct {
	id, name string
	// idx numbers the line across the map, which picks its letter and color; -1 for a folded line.
	idx      int
	weather  scene.Weather
	team     []scene.Climber
	camps    []scene.Camp
	progress float64
	needsYou bool
	selected bool
	dying    float64
	folded   int
}

// hub is a chief's interchange: the station its lines from a to b, exclusive, all run through.
type hub struct {
	id, name string
	a, b     int
	depth    int
	selected bool
}

// network is a group: a repository's lines, or a chief's lines joined at its interchange.
type network struct {
	id, name string
	selected bool
	lines    []line
	hubs     []hub
}

func networks(s scene.Scene) []network {
	idx := 0
	unit := func(p scene.Peak) line {
		l := line{id: p.ID, name: p.Name, idx: idx, weather: p.Weather, team: p.Team, camps: p.Camps,
			progress: progressOf(p.Progress), needsYou: p.NeedsYou, selected: p.ID == s.Selected, dying: scene.Clamp(p.Dying, 0, 1)}
		idx++
		return l
	}
	var out []network
	for _, r := range s.Ranges {
		n := network{id: r.ID, name: r.Name, selected: r.ID == s.Selected}
		var visit func(r scene.Range, depth int)
		visit = func(r scene.Range, depth int) {
			for _, p := range r.Peaks {
				n.lines = append(n.lines, unit(p))
			}
			for _, sub := range r.Ranges {
				a := len(n.lines)
				visit(sub, depth+1)
				n.hubs = append(n.hubs, hub{id: sub.ID, name: sub.Name, a: a, b: len(n.lines), depth: depth + 1, selected: sub.ID == s.Selected})
			}
		}
		visit(r, 0)
		if !strings.HasPrefix(r.ID, "repo:") {
			n.hubs = append(n.hubs, hub{id: r.ID, name: r.Name, a: 0, b: len(n.lines), selected: n.selected})
		}
		out = append(out, n)
	}
	if len(s.Peaks) > 0 {
		var n network
		for _, p := range s.Peaks {
			n.lines = append(n.lines, unit(p))
		}
		out = append(out, n)
	}
	return out
}

func progressOf(f float64) float64 {
	if math.IsNaN(f) {
		return 0
	}
	return scene.Clamp(f, 0, 1)
}

// fold keeps at most k lines of a network, the selected one, those that need you and those being killed first,
// and folds the rest into one last line that carries their work and alerts.
func (n network) fold(k int) network {
	if len(n.lines) <= k {
		return n
	}
	keep := make([]bool, len(n.lines))
	left := k - 1
	for _, pass := range []func(line) bool{
		func(l line) bool { return l.selected },
		func(l line) bool { return l.needsYou },
		func(l line) bool { return l.dying > 0 },
		func(line) bool { return true },
	} {
		for i, l := range n.lines {
			if left > 0 && !keep[i] && pass(l) {
				keep[i], left = true, left-1
			}
		}
	}
	out := network{id: n.id, name: n.name, selected: n.selected}
	at := make([]int, len(n.lines))
	f := line{idx: -1, weather: scene.Clouds}
	var parts []scene.Weather
	for i, l := range n.lines {
		if keep[i] {
			at[i] = len(out.lines)
			out.lines = append(out.lines, l)
			continue
		}
		at[i] = -1
		if f.id == "" || l.selected {
			f.id = l.id
		}
		f.folded++
		f.team = append(f.team, l.team...)
		f.camps = append(f.camps, l.camps...)
		f.progress += l.progress
		f.needsYou = f.needsYou || l.needsYou
		f.selected = f.selected || l.selected
		parts = append(parts, l.weather)
	}
	f.progress /= float64(f.folded)
	f.weather = scene.Combine(scene.Clouds, parts)
	f.name = "+" + itoa(f.folded) + " more"
	last := len(out.lines)
	out.lines = append(out.lines, f)
	for _, h := range n.hubs {
		a, b := len(out.lines), -1
		for i := h.a; i < h.b; i++ {
			j := at[i]
			if j < 0 {
				j = last
			}
			a, b = min(a, j), max(b, j+1)
		}
		if b > a {
			h.a, h.b = a, b
			out.hubs = append(out.hubs, h)
		}
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for ; n > 0; n /= 10 {
		i--
		b[i] = byte('0' + n%10)
	}
	return string(b[i:])
}

// panel is where a network is drawn: its title row at y, then its lines pitch rows apart.
type panel struct {
	net         network
	x, y, w     int
	pitch, jog  int
	dir         int
	nameW, tagW int
}

func (p panel) height() int { return panelHeight(len(p.net.lines), p.pitch, p.jog) }

func panelHeight(lines, pitch, jog int) int { return 1 + lines*pitch + jog }

const (
	margin = 2
	gutter = 4
)

// plan lays the networks out in columns over rows top to bottom: as roomy as fits, lines three rows apart and
// jogging two rows on a diagonal, down to one row apart and straight, then folding lines until the map fits.
func plan(nets []network, width, top, bottom int) []panel {
	rows := bottom - top
	if len(nets) == 0 || rows < 2 || width < 16 {
		return nil
	}
	maxCols := func(minW int) int { return max(1, min(len(nets), (width-2*margin+gutter)/(minW+gutter))) }
	for _, try := range [...]struct{ pitch, jog int }{{3, 2}, {2, 1}, {1, 0}} {
		for cols := 1; cols <= maxCols(40); cols++ {
			if ps, ok := arrange(nets, width, top, bottom, cols, try.pitch, try.jog); ok {
				return ps
			}
		}
	}
	cols := maxCols(28)
	most := 0
	for _, n := range nets {
		most = max(most, len(n.lines))
	}
	for k := most - 1; k >= 1; k-- {
		folded := make([]network, len(nets))
		for i, n := range nets {
			folded[i] = n.fold(k)
		}
		if ps, ok := arrange(folded, width, top, bottom, cols, 1, 0); ok || k == 1 {
			return ps
		}
	}
	ps, _ := arrange(nets, width, top, bottom, cols, 1, 0)
	return ps
}

// arrange deals networks into cols columns, each to the shortest, and reports whether they fit the rows.
func arrange(nets []network, width, top, bottom, cols, pitch, jog int) ([]panel, bool) {
	colW := (width - 2*margin - (cols-1)*gutter) / cols
	if colW < 16 {
		return nil, false
	}
	heights := make([]int, cols)
	ps := make([]panel, len(nets))
	for i, n := range nets {
		c := 0
		for k := range heights {
			if heights[k] < heights[c] {
				c = k
			}
		}
		h := panelHeight(len(n.lines), pitch, jog)
		ps[i] = panel{net: n, x: margin + c*(colW+gutter), y: heights[c], w: colW, pitch: pitch, jog: jog}
		heights[c] += h + 1
	}
	tallest := 0
	for _, h := range heights {
		tallest = max(tallest, h-1)
	}
	rows := bottom - top
	lift := top + max(0, (rows-tallest)/3)
	for i := range ps {
		ps[i].y += lift
		ps[i].fit(seedOf(ps[i].net.id))
	}
	return ps, tallest <= rows
}

// fit sizes a panel's name tags and track: the tag holds a marker, the line's letter, its name, its progress and
// its depot, and the track takes the rest.
func (p *panel) fit(seed uint64) {
	longest := 4
	for _, l := range p.net.lines {
		longest = max(longest, width(l.name))
	}
	p.nameW = max(1, min(longest, 6, p.w-26), min(longest, p.w*2/5-14))
	p.tagW = p.nameW + 14
	if p.w-p.tagW < 16+4*p.jog {
		p.jog = 0
	}
	p.dir = 1
	if seed&1 == 0 && p.jog > 0 {
		p.dir = -1
	}
}

func seedOf(s string) uint64 { return scene.SeedOf(s) }
