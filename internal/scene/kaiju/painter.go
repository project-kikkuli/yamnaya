package kaiju

import (
	"sync"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// painter draws one frame: the backdrop's greys with the monsters and lights added, printed through the film,
// a few colors laid over the print, then the cells and their glyphs.
type painter struct {
	*backdrop
	frame int
	t     float64
	table scene.Rect
	lum   []float32
	px    []rgb
	solid []bool
	tint  []tinted
	// who is, per cell, one more than the index of the monster drawn there.
	who    []int32
	taken  []bool
	text   []bool
	writer int32
	c      *scene.Canvas
	labels *scene.Labels
	bufs   *frameBufs
}

// tinted is a color laid over pixel k of the print, a of the way.
type tinted struct {
	k int32
	a float32
	c rgb
}

type frameBufs struct {
	lum   []float32
	px    []rgb
	solid []bool
	tint  []tinted
	who   []int32
	taken []bool
	text  []bool
	order []int
}

var scratch sync.Pool

func (b *backdrop) at(frame int, table scene.Rect) *painter {
	p := &painter{backdrop: b, frame: frame, t: float64(frame), table: table}
	p.bufs, _ = scratch.Get().(*frameBufs)
	if p.bufs == nil {
		p.bufs = &frameBufs{}
	}
	p.lum = append(p.bufs.lum[:0], b.lum...)
	p.solid = append(p.bufs.solid[:0], b.solid...)
	p.px = grow(p.bufs.px, len(b.lum))
	p.tint = p.bufs.tint[:0]
	cells := b.w * b.p / 2
	p.who, p.taken, p.text = reuse(p.bufs.who, cells), reuse(p.bufs.taken, cells), reuse(p.bufs.text, cells)
	return p
}

func (p *painter) release() {
	if b := p.bufs; b != nil {
		b.lum, b.px, b.solid, b.tint, b.who, b.taken, b.text = p.lum, p.px, p.solid, p.tint, p.who, p.taken, p.text
		p.bufs = nil
		scratch.Put(b)
	}
}

// grow is s at length n, its old contents left for the caller to overwrite.
func grow[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	return s[:n]
}

func reuse[T any](s []T, n int) []T {
	if cap(s) < n {
		return make([]T, n)
	}
	s = s[:n]
	clear(s)
	return s
}

// in reports whether pixel x, y is inside the picture, between the letterbox's bars.
func (p *painter) in(x, y int) bool {
	return x >= 0 && x < p.w && y >= int(p.l.top) && y < int(p.l.bottom)
}

func (p *painter) put(x, y int, v float64) {
	if p.in(x, y) {
		p.lum[y*p.w+x] = float32(v)
	}
}

func (p *painter) add(x, y int, v float64) {
	if p.in(x, y) {
		p.lum[y*p.w+x] += float32(v)
	}
}

// toward moves pixel x, y a of the way to grey v.
func (p *painter) toward(x, y int, v, a float64) {
	if p.in(x, y) && a > 0 {
		k := y*p.w + x
		p.lum[k] += float32(a) * (float32(v) - p.lum[k])
	}
}

func (p *painter) color(x, y int, c rgb, a float64) {
	if p.in(x, y) && a > 0 {
		p.tint = append(p.tint, tinted{k: int32(y*p.w + x), a: float32(min(1, a)), c: c})
	}
}

// grain is the film's noise, a pixel at a time, rolled to a new place each frame.
var grain = func() (out [1 << 13]float32) {
	for i := range out {
		out[i] = float32(.075 * (unit(0x6a1, i) + unit(0x6a2, i) - 1))
	}
	return out
}()

const grainMask = len(grain) - 1

// film prints the frame's greys: the lens darkens the corners, the light flickers, grain crawls, a scratch or a
// fleck of dust runs down the frame now and then, and the bars stay black.
func (p *painter) film() {
	beat := p.frame / 2
	if unit(0x5c, beat) < .5 {
		x, y0 := int(unit(0x5d, beat)*float64(p.w)), int(p.l.top+unit(0x5e, beat)*(p.l.bottom-p.l.top)*.5)
		for y := y0; y < int(p.l.bottom); y++ {
			p.add(x, y, .22)
		}
	}
	if unit(0x5f, p.frame) < .3 {
		x := int(unit(0x60, p.frame) * float64(p.w))
		for y := int(p.l.top); y < int(p.l.bottom); y++ {
			p.toward(x, y, 0, .5)
		}
	}
	for n := range 3 {
		x, y := int(unit(0x61, p.frame*3+n)*float64(p.w)), int(p.l.top+unit(0x62, p.frame*3+n)*(p.l.bottom-p.l.top))
		v := 0.0
		if n == 0 {
			v = 1
		}
		p.put(x, y, v)
		p.put(x+1, y, v)
	}
	flicker := float32(1 + .09*(scene.Noise(0xf11c, p.t*.9)-.5))
	off := int(scene.Spread(0x9a1, p.frame) & uint64(grainMask))
	dx, dy := p.shake()
	top, bottom := int(p.l.top), int(p.l.bottom)-1
	for k := range p.lum {
		if p.vig[k] == 0 {
			p.px[k] = scene.Black
			continue
		}
		src := k
		if dx != 0 || dy != 0 {
			src = min(bottom, max(top, k/p.w+dy))*p.w + min(p.w-1, max(0, k%p.w+dx))
		}
		v := p.lum[src]*p.vig[k]*flicker + grain[(k+off)&grainMask]
		p.px[k] = p.ink[min(255, max(0, int(v*255)))]
	}
	for _, t := range p.tint {
		p.px[t.k] = mix(p.px[t.k], t.c, float64(t.a))
	}
}

func (p *painter) cell(x, y int) rgb { return mix(p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x], .5) }

// canvasOf lays the print on the cells, two pixels to a cell; a terminal with no colors gets blocks of city.
func (p *painter) canvasOf() {
	rows := p.p / 2
	p.c = scene.NewCanvas(p.w, rows, p.table)
	for y := range rows {
		for x := range p.w {
			if p.table.Has(x, y) {
				continue
			}
			up, down := 2*y*p.w+x, (2*y+1)*p.w+x
			if scene.Colors == scene.NoColor {
				switch top, bottom := p.solid[up], p.solid[down]; {
				case top && bottom:
					p.c.Plain(x, y, "█")
				case top:
					p.c.Plain(x, y, "▀")
				case bottom:
					p.c.Plain(x, y, "▄")
				}
				continue
			}
			top, bottom := p.px[up], p.px[down]
			if top == bottom {
				p.c.Set(x, y, top, top, " ")
			} else {
				p.c.Set(x, y, top, bottom, "▀")
			}
		}
	}
	p.labels = scene.NewLabels(p.c)
}

func (p *painter) open(x, y int) bool {
	return x >= 0 && y >= 0 && x < p.w && y < p.p/2 && !p.table.Has(x, y) && !p.taken[y*p.w+x]
}

func (p *painter) beside(x, y int) bool {
	return x > 0 && p.text[y*p.w+x-1] || x+1 < p.w && p.text[y*p.w+x+1]
}

func (p *painter) claim(x, y, n int, text bool) {
	for i := x; i < x+n; i++ {
		k := y*p.w + i
		p.taken[k], p.who[k] = true, p.writer
		p.text[k] = text
	}
}

// mark draws a signal glyph in its own color on a cell backed until it reads, and keeps labels a cell away.
func (p *painter) mark(x, y int, glyph string, fg rgb) bool {
	if !p.open(x, y) || p.beside(x, y) {
		return false
	}
	if scene.Colors == scene.NoColor {
		p.c.Plain(x, y, glyph)
	} else {
		p.c.Set(x, y, fg, scene.Backing(fg, p.cell(x, y)), glyph)
	}
	p.claim(x, y, 1, false)
	p.labels.Reserve(x, y, 1)
	return true
}

// deco draws a glyph that means nothing, readable on its cell, where nothing else is.
func (p *painter) deco(x, y int, glyph string, fg rgb) {
	if !p.open(x, y) || p.beside(x, y) {
		return
	}
	if scene.Colors == scene.NoColor {
		p.c.Plain(x, y, glyph)
	} else {
		f, b := scene.Readable(fg, p.cell(x, y))
		p.c.Set(x, y, f, b, glyph)
	}
	p.claim(x, y, 1, false)
}

// label places text at the first of at where it keeps a cell clear of other labels and glyphs.
func (p *painter) label(text string, fg rgb, at ...[2]int) (int, int, bool) {
	n := utf8.RuneCountInString(text)
	for _, a := range at {
		if !p.free(a[0], a[1], n) {
			continue
		}
		x, y, ok := p.labels.Place(text, fg, a)
		if ok {
			p.claim(x, y, n, true)
			return x, y, true
		}
	}
	return 0, 0, false
}

func (p *painter) free(x, y, n int) bool {
	for i := x; i < x+n; i++ {
		if !p.open(i, y) {
			return false
		}
	}
	return true
}

// wide draws a two-cell glyph as a label is drawn, a cell clear of its neighbors.
func (p *painter) wide(x, y int, glyph string, fg rgb) bool {
	if !p.free(x, y, 2) || !p.labels.Fits(x, y, 2) {
		return false
	}
	if scene.Colors == scene.NoColor {
		p.c.Plain(x, y, glyph)
	} else {
		f, b := scene.Readable(fg, p.cell(x, y))
		p.c.Set(x, y, f, b, glyph)
	}
	p.c.Spill(x+1, y)
	p.labels.Reserve(x, y, 2)
	p.claim(x, y, 2, true)
	return true
}
