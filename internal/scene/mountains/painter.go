package mountains

import (
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
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
	table  scene.Rect
	// lit is the selected mountain's light laid over the backdrop's pixels when a frame is painted.
	lit []pixel
	// pre is the backdrop before its landscape was painted; only limits painting to some columns.
	pre  []rgb
	only []bool
	// keep makes write leave a glyph's color as it is and change only its cell's, for marks whose color is their
	// meaning.
	keep bool
	// bufs holds a frame's buffers for release.
	bufs *frameBufs
}

type pixel struct {
	i int
	c rgb
}

// glyph is a drawn character; a wide one owns the next cell too, marked cont.
type glyph struct {
	r          rune
	s          string
	fg, bg     rgb
	set        bool
	wide, cont bool
}

// layer is one depth of ridgeline: its height per column and which mount owns that column.
type layer struct {
	hm  []float64
	own []int
}

type geom struct{ height, half, x, y float64 }

func newPainter(s scene.Scene, width, height, frame int) *painter {
	p := blankPainter(s, width, height, frame)
	p.l = plan(s, width)
	return p
}

func blankPainter(s scene.Scene, width, height, frame int) *painter {
	th := themeAt(s.Now, s.Dark)
	p := &painter{th: th, w: width, p: height * 2, frame: frame, t: float64(frame)}
	p.px = make([]rgb, p.w*p.p)
	p.solid = make([]bool, p.w*p.p)
	p.front = make([]bool, p.w*p.p)
	p.cells = make([]glyph, p.w*height)
	p.hz = p.p * 64 / 100
	p.top = float64(p.hz) * .07
	return p
}

// backdrop paints everything that holds still: sky, terrain, standing weather, the lake and the pines.
func (p *painter) backdrop() {
	p.terrain()
	p.sky()
	p.halos()
	p.wisps()
	p.air = append([]rgb(nil), p.px...)
	p.ridge()
	p.pre = append([]rgb(nil), p.px...)
	p.landscape()
}

// landscape paints what a selection lights and everything laid over it: the mountains, their standing weather,
// the shore, the lake and the pines.
func (p *painter) landscape() {
	for d := range p.layers {
		p.ground(d)
	}
	p.weather()
	p.shore()
	p.lake()
	p.pines()
}

// paints reports whether column x is painted; a painter limited to some columns leaves the rest as they were.
func (p *painter) paints(x int) bool { return p.only == nil || p.only[x] }

// motion paints what moves over a copy of the backdrop: the slow ambient life of the scene, kills, then the work.
func (p *painter) motion() {
	p.ambient()
	for i, m := range p.l.mounts {
		switch m.weather {
		case scene.Storm:
			p.bolt(i)
		case scene.Eruption:
			p.lava(i)
			p.plume(i)
		}
		if m.dying > 0 {
			p.avalanche(i)
		}
	}
	p.sprites()
}

// at is a copy of a backdrop to paint one frame on; the backdrop's pixels stay as they were.
func (p *painter) at(frame int, table scene.Rect) *painter {
	q := *p
	q.frame, q.t, q.table = frame, float64(frame), table
	q.bufs, _ = scratch.Get().(*frameBufs)
	if q.bufs == nil {
		q.bufs = &frameBufs{}
	}
	q.px = append(q.bufs.px[:0], p.px...)
	for _, d := range p.lit {
		q.px[d.i] = d.c
	}
	q.cells = reuse(q.bufs.cells, len(p.cells))
	q.taken, q.who = q.bufs.taken, q.bufs.who
	return &q
}

// frameBufs are a frame's pixels and cells, used again for a later frame once its canvas is made.
type frameBufs struct {
	px    []rgb
	cells []glyph
	taken []bool
	who   []int
}

var scratch sync.Pool

// release hands the frame's buffers on to the next frame; p is not drawn on after.
func (p *painter) release() {
	if b := p.bufs; b != nil {
		b.px, b.cells, b.taken, b.who = p.px, p.cells, p.taken, p.who
		p.bufs = nil
		scratch.Put(b)
	}
}

// reuse is s cleared to n zero values, kept when it is big enough.
func reuse[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	s = s[:n]
	clear(s)
	return s
}

func (p *painter) set(x, y int, c rgb) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p && p.paints(x) {
		p.px[y*p.w+x] = c
	}
}

func (p *painter) blend(x, y int, c rgb, a float64) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p && a > 0 && p.paints(x) {
		p.px[y*p.w+x] = mix(p.px[y*p.w+x], c, a)
	}
}

func (p *painter) free(x, y int) bool {
	return x >= 0 && x < p.w && y >= 0 && y < p.p/2 && !p.front[2*y*p.w+x] && !p.front[(2*y+1)*p.w+x]
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

// mark writes a glyph whose color is its meaning, changing only its cell to read.
func (p *painter) mark(x, y int, text string, fg rgb) bool {
	p.keep = true
	defer func() { p.keep = false }()
	return p.write(x, y, text, fg)
}

// write puts text on the scene a grapheme at a time, wide ones over two cells, where every cell is free and
// unclaimed, in its own hue on what is behind it, the two moved apart where needed to read, and reports whether
// it fit.
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
		p.clear(x, y)
		behind := mix(p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x], .5)
		f, b := readable(fg, behind)
		if p.keep {
			f, b = fg, backing(fg, behind)
		}
		p.cells[y*p.w+x] = glyph{s: c.s, fg: f, bg: b, set: true, wide: c.width == 2}
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

// layerHeight is how tall the tallest mountain at a depth may stand, nearer ridges lower.
func (p *painter) layerHeight(d int) float64 {
	return (float64(p.hz) - p.top) * (1 - .46*float64(d)/float64(max(1, p.l.depth)))
}

func (p *painter) canvas() *scene.Canvas {
	c := scene.NewCanvas(p.w, p.p/2, p.table)
	for y := range p.p / 2 {
		for x := range p.w {
			switch {
			case p.table.Has(x, y):
			case p.cells[y*p.w+x].cont:
				c.Spill(x, y)
			default:
				p.paintCell(c, x, y)
			}
		}
	}
	return c
}
