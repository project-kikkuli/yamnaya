package pyramids

import (
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// painter lays the desert down in pixels two to a cell, then glyphs on top.
type painter struct {
	pal palette
	l   layout
	w   int
	p   int
	// hz is the pixel row of the desert's edge, nile the first row of the river and bank the near bank's.
	hz, nile, bank int
	// east is how much the sun lights the east faces, 0..1, by the hour.
	east  float64
	hour  float64
	frame int
	px    []rgb
	solid []bool
	cells []glyph
	taken []bool
	// who is, per cell, one more than the index of the pyramid drawn or labelled there.
	who    []int
	writer int
	stars  []star
	table  scene.Rect
	bufs   *frameBufs
}

type star struct {
	i int
	b float64
}

// glyph is a drawn character; a wide one owns the next cell too, marked cont.
type glyph struct {
	s               string
	fg, bg          rgb
	set, wide, cont bool
}

func blankPainter(pal palette, width, height int, hour float64) *painter {
	p := &painter{pal: pal, w: width, p: height * 2, hz: horizon(height), hour: hour}
	below := p.p - p.hz
	p.nile = p.hz + max(6, below*36/100)
	p.bank = p.p - max(2, below/7)
	p.east = smooth(13, 10, hour)
	p.px = make([]rgb, p.w*p.p)
	p.solid = make([]bool, p.w*p.p)
	p.cells = make([]glyph, p.w*height)
	return p
}

// at is a copy of a backdrop to paint one frame on; the backdrop's pixels stay as they were.
func (p *painter) at(frame int, table scene.Rect) *painter {
	q := *p
	q.frame, q.table = frame, table
	q.bufs, _ = scratch.Get().(*frameBufs)
	if q.bufs == nil {
		q.bufs = &frameBufs{}
	}
	q.px = append(q.bufs.px[:0], p.px...)
	q.solid = append(q.bufs.solid[:0], p.solid...)
	q.cells = reuse(q.bufs.cells, len(p.cells))
	q.taken = reuse(q.bufs.taken, len(p.cells))
	q.who = reuse(q.bufs.who, len(p.cells))
	return &q
}

type frameBufs struct {
	px    []rgb
	solid []bool
	cells []glyph
	taken []bool
	who   []int
	row   []rgb
	order []int
}

var scratch sync.Pool

// release hands the frame's buffers on to the next frame; p is not drawn on after.
func (p *painter) release() {
	if b := p.bufs; b != nil {
		b.px, b.solid, b.cells, b.taken, b.who = p.px, p.solid, p.cells, p.taken, p.who
		p.bufs = nil
		scratch.Put(b)
	}
}

func reuse[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	s = s[:n]
	clear(s)
	return s
}

func (p *painter) in(x, y int) bool { return x >= 0 && x < p.w && y >= 0 && y < p.p }

func (p *painter) set(x, y int, c rgb) {
	if p.in(x, y) {
		p.px[y*p.w+x] = c
	}
}

// fill sets a solid pixel, one that NoColor draws as a block.
func (p *painter) fill(x, y int, c rgb) {
	if p.in(x, y) {
		p.px[y*p.w+x], p.solid[y*p.w+x] = c, true
	}
}

func (p *painter) blend(x, y int, c rgb, a float64) {
	if p.in(x, y) && a > 0 {
		p.px[y*p.w+x] = mix(p.px[y*p.w+x], c, a)
	}
}

// free reports whether a glyph may go in cell x, y: on screen, clear of the table, and not yet written.
func (p *painter) free(x, y int) bool {
	return x >= 0 && x < p.w && y >= 0 && y < p.p/2 && !p.table.Has(x, y) && !p.taken[y*p.w+x]
}

func (p *painter) behind(x, y int) rgb { return mix(p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x], .5) }

// claim marks cell x, y for the pyramid being written, so a click there selects it.
func (p *painter) claim(x, y int) {
	if x >= 0 && x < p.w && y >= 0 && y < p.p/2 && p.writer > 0 {
		p.who[y*p.w+x] = p.writer
	}
}

// write puts text on the scene a grapheme at a time, wide ones over two cells, where every cell is free,
// readable against what is behind it, and reports whether it fit.
func (p *painter) write(x, y int, text string, fg rgb) bool {
	return p.text(x, y, text, fg, false)
}

// text is write, with signal glyphs keeping their color and their cell moved to read behind them.
func (p *painter) text(x, y int, text string, fg rgb, signal bool) bool {
	if y < 0 || y >= p.p/2 || x < 0 {
		return false
	}
	n := 0
	for rest := text; rest != ""; {
		c, width := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(c):]
		if width < 1 || width > 2 || c[0] < ' ' || c[0] == 0x7f {
			continue
		}
		for k := range width {
			if !p.free(x+n+k, y) {
				return false
			}
		}
		n += width
	}
	for rest := text; rest != ""; {
		c, width := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(c):]
		if width < 1 || width > 2 || c[0] < ' ' || c[0] == 0x7f {
			continue
		}
		p.cell(x, y, c, fg, signal, width == 2)
		if width == 2 {
			p.cells[y*p.w+x+1] = glyph{set: true, cont: true}
			p.taken[y*p.w+x+1] = true
		}
		x += width
	}
	return true
}

// put draws one narrow glyph in cell x, y if it is free, readable against what is behind it.
func (p *painter) put(x, y int, g string, fg rgb) bool {
	if !p.free(x, y) {
		return false
	}
	p.cell(x, y, g, fg, false, false)
	return true
}

// mark is put for a glyph whose color is its meaning: busy, waiting, needs you, a pull request's CI.
func (p *painter) mark(x, y int, g string, fg rgb) bool {
	if !p.free(x, y) {
		return false
	}
	p.cell(x, y, g, fg, true, false)
	return true
}

func (p *painter) cell(x, y int, g string, fg rgb, signal, wide bool) {
	i := y*p.w + x
	bg := p.behind(x, y)
	if signal {
		bg = scene.Backing(fg, bg)
	} else {
		fg, bg = scene.Readable(fg, bg)
	}
	p.cells[i] = glyph{s: g, fg: fg, bg: bg, set: true, wide: wide}
	p.taken[i] = true
	p.claim(x, y)
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

// paintCell puts cell x, y on c: its glyph, or the two pixels it holds as a half block.
func (p *painter) paintCell(c *scene.Canvas, x, y int) {
	top, bottom := p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x]
	g := p.cells[y*p.w+x]
	if scene.Colors == scene.NoColor {
		if g.set {
			c.Plain(x, y, g.s)
			return
		}
		up, down := p.solid[2*y*p.w+x], p.solid[(2*y+1)*p.w+x]
		switch {
		case up && down:
			c.Plain(x, y, "█")
		case up:
			c.Plain(x, y, "▀")
		case down:
			c.Plain(x, y, "▄")
		default:
			c.Plain(x, y, " ")
		}
		return
	}
	switch {
	case g.set:
		c.Set(x, y, g.fg, g.bg, g.s)
	case scene.Code(top) == scene.Code(bottom):
		c.Set(x, y, top, top, " ")
	default:
		c.Set(x, y, top, bottom, "▀")
	}
}
