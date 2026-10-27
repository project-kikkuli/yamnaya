package islands

import (
	"math"
	"sync"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// painter draws one frame over a copy of the backdrop's pixels, two to a cell, then glyphs on top.
type painter struct {
	*backdrop
	l     layout
	frame int
	t     float64
	table scene.Rect
	px    []rgb
	cells []glyph
	taken []bool
	// who is, per cell, one more than the index of the island whose glyphs are there, or minus one more than
	// the group's.
	who    []int32
	writer int32
	// keep holds a write's color as it is, moving only what is behind it until the two read apart.
	keep bool
	bufs *frameBufs
}

type glyph struct {
	s    string
	fg   rgb
	bg   rgb
	set  bool
	cont bool
}

type frameBufs struct {
	px    []rgb
	cells []glyph
	taken []bool
	who   []int32
}

var scratch sync.Pool

// swell is how a wave's pass lifts the water's light, over the 32 steps of its phase.
var swell = func() (out [32]float64) {
	for i := range out {
		out[i] = 1 + .06*math.Sin(2*math.Pi*float64(i)/32)
	}
	return out
}()

// at readies p to paint frame over the table's box: the backdrop with the swell rolled on to that frame.
func (p *painter) at(frame int, table scene.Rect) *painter {
	p.frame, p.t, p.table = frame, float64(frame), table
	p.bufs, _ = scratch.Get().(*frameBufs)
	if p.bufs == nil {
		p.bufs = &frameBufs{}
	}
	p.px = reuse(p.bufs.px, len(p.backdrop.px))
	for k, c := range p.backdrop.px {
		if ph := p.phase[k]; ph != noSwell {
			c = c.Scale(swell[(int(ph)+frame)&31])
		}
		p.px[k] = c
	}
	cells := p.w * p.p / 2
	p.cells, p.taken, p.who = reuse(p.bufs.cells, cells), reuse(p.bufs.taken, cells), reuse(p.bufs.who, cells)
	return p
}

// release hands the frame's buffers on to the next frame; p is not drawn on after.
func (p *painter) release() {
	if b := p.bufs; b != nil {
		b.px, b.cells, b.taken, b.who = p.px, p.cells, p.taken, p.who
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

func (p *painter) blend(k int, c rgb, a float64) {
	if a > 0 {
		p.px[k] = mix(p.px[k], c, a)
	}
}

// motion paints what moves: surf on the shoals, the weather passing over each island, a kill sinking one, the
// selection's outline; then the work on top.
func (p *painter) motion() {
	p.surf()
	for i := range p.l.isles {
		is := &p.l.isles[i]
		if is.dying > 0 {
			p.sink(i)
			continue
		}
		switch is.weather {
		case scene.Clouds:
			p.clouds(i)
		case scene.Storm:
			p.stormCell(i)
		case scene.Eruption:
			p.lava(i)
			p.smoke(i)
		}
		if is.selected {
			for _, s := range p.fx[i].shore {
				p.blend(int(s.k), p.pal.rim, .6)
			}
		}
	}
	p.sprites()
}

func (p *painter) surf() {
	for i := range p.fx {
		for _, s := range p.fx[i].shore {
			wave := .5 + .5*math.Sin(float64(s.ph)-p.t*.5)
			h := float64(p.field[s.k])
			p.blend(int(s.k), p.pal.foam, smooth(.35, 1, wave)*.55*smooth(-.16, -.02, h))
		}
	}
}

// clouds drift over island i from the west, their shadows falling on the water beneath them.
func (p *painter) clouds(i int) {
	is := &p.l.isles[i]
	reach := is.r*1.7 + 3
	drift := p.t*.35 + unit(is.seed, 11)*40
	p.each(is.cx, is.cy, reach, func(x, y, k int) {
		fx, fy := float64(x)+.5, float64(y)+.5
		env := smooth(reach, reach*.45, math.Hypot(fx-is.cx, fy-is.cy))
		if env <= 0 {
			return
		}
		shade := scene.Fbm2(is.seed+31, (fx-3-drift)*.13, (fy-3)*.2, 2)
		p.px[k] = p.px[k].Scale(1 - .3*smooth(.46, .64, shade)*env)
		p.blend(k, p.pal.cloud, smooth(.44, .64, scene.Fbm2(is.seed+31, (fx-drift)*.13, fy*.2, 2))*env*.92)
	})
}

// stormCell churns a dark cloud over island i that a bolt of lightning lights now and then.
func (p *painter) stormCell(i int) {
	is := &p.l.isles[i]
	reach := is.r*1.5 + 3
	flash := p.flash(is)
	p.each(is.cx, is.cy, reach, func(x, y, k int) {
		fx, fy := float64(x)+.5, float64(y)+.5
		env := smooth(reach, reach*.4, math.Hypot(fx-is.cx, fy-is.cy))
		if env <= 0 {
			return
		}
		a := smooth(.34, .58, scene.Fbm2(is.seed+41, (fx-p.t*.12)*.16, (fy+p.t*.05)*.22, 2)) * env
		c := p.pal.storm
		if flash {
			c = mix(c, p.pal.bolt, .45)
		}
		p.blend(k, c, a*.88)
	})
}

func (p *painter) flash(is *isle) bool { return (p.frame+int(unit(is.seed, 12)*14))%14 < 2 }

func (p *painter) lava(i int) {
	for _, s := range p.fx[i].lava {
		p.px[s.k] = mix(p.pal.lava, p.pal.lavaHot, .5+.5*math.Sin(float64(s.ph)+p.t*.7))
	}
}

// smoke rises from island i's crater in puffs that grow and thin as the wind carries them east.
func (p *painter) smoke(i int) {
	is := &p.l.isles[i]
	for n := range 6 {
		age := math.Mod(p.t*.35+float64(n)*2.1, 12.6)
		sx := is.cx + age*1.1 + math.Sin(age*.7+float64(n))*1.2
		sy := is.cy - age*.9
		rad := 1.8 + age*.45
		a := (1 - age/12.6) * .75
		p.each(sx, sy, rad, func(x, y, k int) {
			p.blend(k, p.pal.smoke, a*smooth(rad, rad*.3, math.Hypot(float64(x)+.5-sx, float64(y)+.5-sy)))
		})
	}
}

// sink raises the sea over a killed island as its kill plays: the low ground floods first, foam rides the
// waterline up the hills, rings ripple out, and by the end only open water is left.
func (p *painter) sink(i int) {
	is := &p.l.isles[i]
	d := is.dying
	level := d*1.25 - .08
	fade := 1 - smooth(.75, 1, d)
	for _, lp := range p.fx[i].land {
		h := float64(lp.h)
		if h >= level+.06 {
			continue
		}
		if h >= level {
			p.blend(int(lp.k), p.pal.foam, .55*fade)
			continue
		}
		c := mix(lp.sea, p.pal.shoal, smooth(level-.4, level, h)*.6*fade)
		p.px[lp.k] = c
	}
	r1, r2 := is.r*(1.05+1.8*d), is.r*(.7+1.8*d)
	p.each(is.cx, is.cy, r1+2, func(x, y, k int) {
		if p.own[k] == int16(i) && float64(p.field[k]) >= level {
			return
		}
		dist := math.Hypot(float64(x)+.5-is.cx, float64(y)+.5-is.cy)
		p.blend(k, p.pal.foam, (1-d)*.55*(smooth(1.3, 0, math.Abs(dist-r1))+.7*smooth(1.3, 0, math.Abs(dist-r2))))
	})
}

func (p *painter) free(x, y int) bool {
	return x >= 0 && y >= 0 && x < p.w && y < p.p/2 && !p.table.Has(x, y) && !p.taken[y*p.w+x]
}

// write puts text on the scene a grapheme at a time, wide ones over two cells, where every cell is free, in a
// color that reads against what is behind it, and reports whether it fit.
func (p *painter) write(x, y int, text string, fg rgb) bool {
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
	if n == 0 {
		return false
	}
	var bg rgb
	for k := range n {
		c := mix(p.px[2*y*p.w+x+k], p.px[(2*y+1)*p.w+x+k], .5).Scale(1 / float64(n))
		bg = rgb{R: bg.R + c.R, G: bg.G + c.G, B: bg.B + c.B}
	}
	if !p.keep {
		fg, _ = scene.Readable(fg, bg)
	}
	for rest := text; rest != ""; {
		c, width := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(c):]
		if width < 1 || width > 2 || c[0] < ' ' || c[0] == 0x7f {
			continue
		}
		i := y*p.w + x
		p.cells[i] = glyph{s: c, fg: fg, bg: scene.Backing(fg, mix(p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x], .5)), set: true}
		p.taken[i], p.who[i] = true, p.writer
		if width == 2 {
			p.cells[i+1] = glyph{set: true, cont: true}
			p.taken[i+1], p.who[i+1] = true, p.writer
		}
		x += width
	}
	return true
}

// mark writes a signal in its own color, so red stays red and green green on any ground.
func (p *painter) mark(x, y int, text string, fg rgb) bool {
	p.keep = true
	defer func() { p.keep = false }()
	return p.write(x, y, text, fg)
}

func (p *painter) canvas() *scene.Canvas {
	rows := p.p / 2
	c := scene.NewCanvas(p.w, rows, p.table)
	for y := range rows {
		for x := range p.w {
			if p.table.Has(x, y) {
				continue
			}
			i := y*p.w + x
			g := p.cells[i]
			if g.cont {
				c.Spill(x, y)
				continue
			}
			up, down := 2*y*p.w+x, (2*y+1)*p.w+x
			if scene.Colors == scene.NoColor {
				switch top, bottom := p.field[up] >= 0, p.field[down] >= 0; {
				case g.set:
					c.Plain(x, y, g.s)
				case top && bottom:
					c.Plain(x, y, "█")
				case top:
					c.Plain(x, y, "▀")
				case bottom:
					c.Plain(x, y, "▄")
				default:
					c.Plain(x, y, " ")
				}
				continue
			}
			top, bottom := p.px[up], p.px[down]
			switch {
			case g.set:
				c.Set(x, y, g.fg, g.bg, g.s)
			case scene.Code(top) == scene.Code(bottom):
				c.Set(x, y, top, top, " ")
			default:
				c.Set(x, y, top, bottom, "▀")
			}
		}
	}
	return c
}
