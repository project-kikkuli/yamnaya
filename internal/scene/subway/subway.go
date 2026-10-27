package subway

import (
	"math"
	"strings"
	"sync"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Subway draws the board as a transit map: each group a network, each unit a colored line whose trains are
// its agents and whose stations are its pull requests, and each chief an interchange joining its goals' lines.
type Subway struct{}

func init() { scene.Register(Subway{}) }

func (Subway) Name() string { return "subway" }

func (Subway) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	m := still(s, width, height, box)
	sh, _ := frames.Get().(*sheet)
	if sh == nil {
		sh = &sheet{}
	}
	sh.copyFrom(&m.sheet)
	m.motion(sh, frame)
	c := sh.canvas(box)
	frames.Put(sh)
	return c
}

func (Subway) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	m := still(s, width, height, box)
	if x < 0 || y < 0 || x >= width || y >= height {
		return ""
	}
	if r := m.owner[y*width+x]; r > 0 {
		return m.ids[r-1]
	}
	return ""
}

var frames sync.Pool

// board is the map as it holds still: everything but the trains, the weather's motion, blinking alerts and
// lines being killed, which each frame draws over a copy.
type board struct {
	sheet
	pal   palette
	geos  []geo
	owner []int32
	ids   []string
}

var boards struct {
	sync.Mutex
	key uint64
	b   *board
}

func still(s scene.Scene, width, height int, box scene.Rect) *board {
	key := keyOf(s, width, height, box)
	boards.Lock()
	defer boards.Unlock()
	if boards.b == nil || boards.key != key {
		boards.key, boards.b = key, build(s, width, height, box)
	}
	return boards.b
}

func build(s scene.Scene, width, height int, box scene.Rect) *board {
	b := &board{sheet: newSheet(width, height), pal: paletteAt(s.Now, s.Dark), owner: make([]int32, width*height)}
	top, bottom := 0, height
	if box.X1 > box.X0 && box.Y0-1 >= 5 {
		bottom = box.Y0 - 1
	}
	ps := plan(networks(s), width, top, bottom)
	b.geos = geos(ps)
	b.ground(ps, bottom)
	ref := func(id string) func(x, y int) {
		if id == "" {
			return nil
		}
		b.ids = append(b.ids, id)
		r := int32(len(b.ids))
		return func(x, y int) {
			if x >= 0 && y >= 0 && x < b.w && y < b.h {
				b.owner[y*b.w+x] = r
			}
		}
	}
	for _, p := range ps {
		own := ref(p.net.id)
		if own == nil {
			continue
		}
		for y := p.y; y < p.y+p.height(); y++ {
			for x := p.x - 1; x <= p.x+p.w; x++ {
				own(x, y)
			}
		}
	}
	for i := range b.geos {
		if g := &b.geos[i]; g.l.selected && g.l.dying == 0 {
			b.pal.glow(&b.sheet, g)
		}
	}
	for i := range b.geos {
		g := &b.geos[i]
		own := ref(g.l.id)
		g.ref = int32(len(b.ids))
		if g.l.dying > 0 {
			ownTrack(g, own)
			continue
		}
		b.pal.track(&b.sheet, g, own)
		b.pal.tag(&b.sheet, g, own)
	}
	for pi, p := range ps {
		b.titles(p, pi, ref)
	}
	for i := range b.geos {
		if g := &b.geos[i]; g.l.dying == 0 {
			b.pal.stops(&b.sheet, g)
		}
	}
	return b
}

// ownTrack claims the cells of a line being killed for it, as they were when it lived.
func ownTrack(g *geo, own func(x, y int)) {
	for x := g.tagX; x < g.x1; x++ {
		py := g.py(max(x, g.x0))
		own(x, py/2)
		own(x, (py+g.th-1)/2)
	}
}

// ground lays the paper: the map's plain, a tinted district under each network, and a river winding across.
func (b *board) ground(ps []panel, bottom int) {
	for i := range b.px {
		b.px[i] = b.pal.paper
	}
	seed := uint64(0x5ab)
	mid := float64(bottom) * 1.25
	for x := range b.w {
		c := mid + (scene.Fbm(seed, float64(x)*.025, 2)-.5)*float64(bottom)*1.1
		for y := int(c) - 2; y <= int(c)+2; y++ {
			edge := 1 - math.Abs(float64(y)-c)/3
			b.blend(x, y, b.pal.river, scene.Clamp(edge*1.6, 0, 1))
		}
	}
	for _, p := range ps {
		if p.net.id == "" {
			continue
		}
		for y := 2 * p.y; y < 2*(p.y+p.height()); y++ {
			for x := p.x - 1; x <= p.x+p.w; x++ {
				b.blend(x, y, b.pal.district, .7)
			}
		}
	}
}

// titles writes a network's name over it and draws its chiefs' interchanges, nested ones labelled below.
func (b *board) titles(p panel, pi int, ref func(string) func(x, y int)) {
	var gs []geo
	first := -1
	for i := range b.geos {
		if b.geos[i].pn == pi {
			if first < 0 {
				first = i
			}
			gs = append(gs, b.geos[i])
		}
	}
	if p.net.id != "" {
		x := p.x
		if p.net.selected {
			b.over(x, p.y, "▸", b.pal.accent)
		}
		x += 2
		chief := !strings.HasPrefix(p.net.id, "repo:")
		if chief {
			b.over(x, p.y, "⇄", b.pal.ink)
			x += 2
		}
		ink := b.pal.ink
		if p.net.selected {
			ink = b.pal.accent
		}
		x += b.text(x, p.y, truncate(p.net.name, p.w-(x-p.x)-2), ink, p.w-(x-p.x)-2)
		for x++; x < p.x+p.w; x++ {
			b.over(x, p.y, "─", b.pal.rule)
		}
	}
	for _, h := range p.net.hubs {
		if h.b <= h.a || first < 0 {
			continue
		}
		x0, x1 := gs[0].x0, gs[0].x1
		x := hubX(x0, x1, h.depth)
		own := ref(h.id)
		b.pal.interchange(&b.sheet, gs[h.a:h.b], h, x, own)
		if h.depth == 0 {
			continue
		}
		y := max(gs[h.a].row(x), gs[h.b-1].row(x)) + 1
		label := "⇄ " + h.name
		n := min(width(label), p.x+p.w-x)
		ok := n > 3
		for k := x - 1; k < x+n && ok; k++ {
			ok = b.free(k, y) && (y < p.y+p.height())
			for i := range gs {
				ok = ok && gs[i].row(k) != y
			}
		}
		if !ok {
			continue
		}
		ink := b.pal.name
		if h.selected {
			ink = b.pal.accent
			b.over(x-1, y, "▸", b.pal.accent)
		}
		b.text(x, y, truncate(label, n), ink, n)
		if own != nil {
			for k := x; k < x+n; k++ {
				own(k, y)
			}
		}
	}
}

// motion draws what moves on a copy of the still map: lines being killed, the weather, trains and blinking
// stations.
func (b *board) motion(s *sheet, frame int) {
	for i := range b.geos {
		g := &b.geos[i]
		if g.l.dying > 0 {
			b.pal.track(s, g, nil)
			b.pal.tag(s, g, nil)
			b.pal.stops(s, g)
			continue
		}
		b.weather(s, g, frame)
	}
	for i := range b.geos {
		g := &b.geos[i]
		if g.l.dying > 0 {
			continue
		}
		if g.l.weather != scene.Eruption {
			b.trains(s, g, frame)
		}
		if g.l.needsYou && frame/2%2 == 0 {
			x := g.x1 - 1
			s.put(x, g.row(x), "!", scene.White, scene.Backing(scene.White, b.pal.red))
		}
	}
}

// trains runs the line's busy agents back and forth along it, each a car with its nose the way it goes.
func (b *board) trains(s *sheet, g *geo, frame int) {
	n := min(stanceCount(g.l.team, scene.Climbing), max(1, g.span()/8))
	span := max(1, g.span()-4)
	period := 2 * span
	start := int(scene.Unit(g.seed, 7) * float64(period))
	band := b.pal.band(g)
	car := carOn(band)
	band = scene.Backing(car, band)
	for k := range n {
		pos := (frame + start + k*period/max(1, n)) % period
		parts, p := [3]string{"▐", "█", "▶"}, pos
		if pos >= span {
			parts, p = [3]string{"◀", "█", "▌"}, period-1-pos
		}
		x := g.x0 + p
		for i, glyph := range parts {
			if c := x + i; s.free(c, g.row(c)) {
				s.put(c, g.row(c), glyph, car, band)
			}
		}
	}
}

// weather moves over a line: drifting cloud for delays, a dark cloud with rain and lightning for a disruption,
// smoke over a suspended line, and sparkles round a line that opened.
func (b *board) weather(s *sheet, g *geo, frame int) {
	t := float64(frame)
	L := float64(g.span())
	switch g.l.weather {
	case scene.Clouds:
		for k := range 1 + g.span()/24 {
			cx := float64(g.x0) + math.Mod(scene.Unit(g.seed, k)*L+t*.3, L)
			puff(s, cx, float64(g.py(int(cx)))-1, 4+2*scene.Unit(g.seed, 10+k), 1.2*float64(g.th), b.pal.cloud, .45)
		}
	case scene.Storm:
		for k := range 1 + g.span()/30 {
			cx := float64(g.x0) + math.Mod(scene.Unit(g.seed, k)*L+t*.6, L)
			cy := float64(g.py(int(cx))) - 4
			for x := int(cx) - 5; x <= int(cx)+5; x++ {
				if y := g.row(x) - 1; g.th == 2 && (x+frame)%3 == 0 && s.free(x, y) {
					s.over(x, y, "╱", b.pal.rain)
				}
			}
			puff(s, cx, cy, 7, 1.4*float64(g.th), b.pal.storm, .85)
			if (frame+k*5+int(g.seed&15))%14 == 0 {
				x := int(cx)
				if y := g.row(x) - 1; g.th == 2 && y >= 0 {
					s.over(x, y, "ϟ", b.pal.bolt)
				}
			}
		}
	case scene.Eruption:
		for k := range 1 + g.span()/20 {
			cx := float64(g.x0) + scene.Unit(g.seed, k)*L
			rise := math.Mod(t*.5+scene.Unit(g.seed, 20+k)*8, 8)
			puff(s, cx+rise*.4, float64(g.py(int(cx)))-1-rise, 2.5+rise*.3, 1.6+rise*.15, b.pal.smoke, .8*(1-rise/8))
		}
	case scene.Summit:
		for k := range 4 {
			x := g.x1 - 3 - int(scene.Spread(g.seed, k)%uint64(max(1, min(g.span()-4, 18))))
			y := g.row(x) - 1
			if g.th == 1 || !s.free(x, y) {
				continue
			}
			glyph := "✧"
			if (frame+k*3)%6 < 3 {
				glyph = "✦"
			}
			s.over(x, y, glyph, b.pal.gold)
		}
	}
}

// puff blends a soft ellipse of color into the pixels round cx, cy.
func puff(s *sheet, cx, cy, rx, ry float64, c rgb, a float64) {
	for y := int(cy - ry); y <= int(cy+ry)+1; y++ {
		for x := int(cx - rx); x <= int(cx+rx)+1; x++ {
			dx, dy := (float64(x)+.5-cx)/rx, (float64(y)+.5-cy)/ry
			if d := dx*dx + dy*dy; d < 1 {
				s.blend(x, y, c, a*(1-d))
			}
		}
	}
}
