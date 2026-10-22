package scene

import (
	"math"

	"github.com/charmbracelet/x/ansi"
)

// painter lays the scene down in pixels two to a cell, then glyphs on top.
type painter struct {
	th    theme
	l     layout
	w, p  int
	hz    int
	top   float64
	frame int
	t     float64
	px    []rgb
	air   []rgb
	solid []bool
	front []bool
	cells []glyph
	taken []bool
	// who is, per cell, one more than the index of the mount whose name, team or camps are written there.
	who    []int
	writer int
	layers []layer
	far    []float64
	geo    []geom
	table  rect
}

// glyph is a drawn character; a wide one owns the next cell too, marked cont.
type glyph struct {
	r          rune
	s          string
	fg         rgb
	set        bool
	wide, cont bool
}

// layer is one depth of ridgeline: its height per column and which mount owns that column.
type layer struct {
	hm  []float64
	own []int
}

type geom struct{ height, half, x, y float64 }

type rect struct{ x0, y0, x1, y1 int }

func (r rect) has(x, y int) bool { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }

func newPainter(s Scene, width, height, frame int, table rect) *painter {
	th := themeAt(s.Now, s.Dark)
	p := &painter{th: th, w: width, p: height * 2, frame: frame, t: float64(frame), table: table}
	p.px = make([]rgb, p.w*p.p)
	p.solid = make([]bool, p.w*p.p)
	p.front = make([]bool, p.w*p.p)
	p.cells = make([]glyph, p.w*height)
	p.hz = p.p * 72 / 100
	if table.y1 > table.y0 {
		p.hz = int(clamp(float64(table.y0*2+2), float64(p.p)*.5, float64(p.p)*.8))
	}
	p.top = float64(p.hz) * .07
	p.l = plan(s, width)
	return p
}

func (p *painter) paint() {
	p.terrain()
	p.sky()
	p.halos()
	p.wisps()
	p.air = append([]rgb(nil), p.px...)
	p.ridge()
	for d := range p.layers {
		p.ground(d)
	}
	p.weather()
	p.shore()
	p.lake()
	p.pines()
	p.sprites()
}

func (p *painter) set(x, y int, c rgb) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p {
		p.px[y*p.w+x] = c
	}
}

func (p *painter) blend(x, y int, c rgb, a float64) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p && a > 0 {
		p.px[y*p.w+x] = mix(p.px[y*p.w+x], c, a)
	}
}

func (p *painter) free(x, y int) bool {
	return x >= 0 && x < p.w && y >= 0 && y < p.p/2 && !p.table.has(x, y) && !p.front[2*y*p.w+x] && !p.front[(2*y+1)*p.w+x]
}

// clear empties a cell, and the other half of any wide glyph it held.
func (p *painter) clear(x, y int) {
	i := y*p.w + x
	switch {
	case p.cells[i].cont:
		p.cells[i-1] = glyph{}
	case p.cells[i].wide:
		p.cells[i+1] = glyph{}
	}
	p.cells[i] = glyph{}
}

// write puts text on the scene a grapheme at a time, wide ones over two cells, where every cell is free and
// unclaimed, in a color that reads against what is behind it, and reports whether it fit.
func (p *painter) write(x, y int, text string, fg rgb) bool {
	if y < 0 || y >= p.p/2 || x < 0 {
		return false
	}
	type part struct {
		s     string
		width int
	}
	var parts []part
	n := 0
	for rest := ansi.Strip(text); rest != ""; {
		c, width := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(c):]
		if width < 1 || width > 2 || c[0] < ' ' || c[0] == 0x7f {
			continue
		}
		for k := range width {
			if !p.free(x+n+k, y) || p.taken[y*p.w+x+n+k] {
				return false
			}
		}
		parts = append(parts, part{c, width})
		n += width
	}
	for _, c := range parts {
		bg := mix(p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x], .5)
		p.clear(x, y)
		p.cells[y*p.w+x] = glyph{s: c.s, fg: readable(fg, bg), set: true, wide: c.width == 2}
		p.taken[y*p.w+x] = true
		p.who[y*p.w+x] = p.writer
		if c.width == 2 {
			p.clear(x+1, y)
			p.cells[y*p.w+x+1] = glyph{set: true, cont: true}
			p.taken[y*p.w+x+1] = true
		}
		x += c.width
	}
	return true
}

// readable keeps a color's hue but moves it away from the background until the two contrast.
func readable(fg, bg rgb) rgb {
	if math.Abs(fg.luma()-bg.luma()) >= .4 {
		return fg
	}
	if bg.luma() > .5 {
		return fg.scale(.3)
	}
	return mix(fg, white, .6)
}

// layerHeight is how tall the tallest mountain at a depth may stand, nearer ridges lower.
func (p *painter) layerHeight(d int) float64 {
	return (float64(p.hz) - p.top) * (1 - .46*float64(d)/float64(max(1, p.l.depth)))
}
