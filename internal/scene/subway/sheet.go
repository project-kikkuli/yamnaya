package subway

import (
	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sheet is the map being drawn: pixels two to a cell under glyphs set on top.
type sheet struct {
	w, h  int
	px    []rgb
	cells []cell
	// taken marks cells a station, train, sign or text holds, which nothing later draws over.
	taken []bool
}

// cell is a glyph over the pixels; solid gives it its own background. plain is the shape of the pixels under
// NoColor when no glyph is set.
type cell struct {
	s      string
	fg, bg rgb
	set    bool
	solid  bool
	cont   bool
	plain  string
}

func newSheet(w, h int) sheet {
	return sheet{w: w, h: h, px: make([]rgb, w*h*2), cells: make([]cell, w*h), taken: make([]bool, w*h)}
}

func (s *sheet) in(x, y int) bool { return x >= 0 && y >= 0 && x < s.w && y < s.h }

func (s *sheet) free(x, y int) bool { return s.in(x, y) && !s.taken[y*s.w+x] }

func (s *sheet) pix(x, py int, c rgb) {
	if x >= 0 && py >= 0 && x < s.w && py < 2*s.h {
		s.px[py*s.w+x] = c
	}
}

func (s *sheet) blend(x, py int, c rgb, a float64) {
	if x >= 0 && py >= 0 && x < s.w && py < 2*s.h && a > 0 {
		i := py*s.w + x
		s.px[i] = mix(s.px[i], c, a)
	}
}

// back is what is behind the glyph at x, y: its own background, or the two pixels it covers.
func (s *sheet) back(x, y int) rgb {
	if c := s.cells[y*s.w+x]; c.set && c.solid {
		return c.bg
	}
	return mix(s.px[2*y*s.w+x], s.px[(2*y+1)*s.w+x], .5)
}

// put sets a glyph with its own background and claims the cell.
func (s *sheet) put(x, y int, glyph string, fg, bg rgb) {
	if s.in(x, y) {
		s.cells[y*s.w+x] = cell{s: glyph, fg: fg, bg: bg, set: true, solid: true}
		s.taken[y*s.w+x] = true
	}
}

// over sets a glyph on the pixels behind it and claims the cell.
func (s *sheet) over(x, y int, glyph string, fg rgb) {
	if s.in(x, y) {
		s.cells[y*s.w+x] = cell{s: glyph, fg: fg, set: true}
		s.taken[y*s.w+x] = true
	}
}

func (s *sheet) shape(x, y int, glyph string) {
	if s.in(x, y) {
		s.cells[y*s.w+x].plain = glyph
	}
}

// text writes up to limit cells of text a grapheme at a time, wide ones over two cells, in a color that reads
// against what is behind it, and is how many cells it took.
func (s *sheet) text(x, y int, text string, fg rgb, limit int) int {
	if !s.in(x, y) {
		return 0
	}
	n := 0
	for rest := ansi.Strip(text); rest != ""; {
		c, w := ansi.FirstGraphemeCluster(rest, ansi.GraphemeWidth)
		rest = rest[len(c):]
		if w < 1 || w > 2 || c[0] < ' ' || c[0] == 0x7f {
			continue
		}
		if n+w > limit || x+n+w > s.w {
			break
		}
		if len(c) == 1 {
			c = scene.Glyph(rune(c[0]))
		}
		f, b := scene.Readable(fg, s.back(x+n, y))
		s.put(x+n, y, c, f, b)
		if w == 2 {
			s.cells[y*s.w+x+n+1] = cell{set: true, cont: true}
			s.taken[y*s.w+x+n+1] = true
		}
		n += w
	}
	return n
}

func width(text string) int { return ansi.StringWidth(ansi.Strip(text)) }

// truncate is text cut to n cells, an ellipsis marking the cut.
func truncate(text string, n int) string { return ansi.Truncate(ansi.Strip(text), n, "…") }

// copyFrom makes s a copy of o, reusing its buffers.
func (s *sheet) copyFrom(o *sheet) {
	s.w, s.h = o.w, o.h
	s.px = append(s.px[:0], o.px...)
	s.cells = append(s.cells[:0], o.cells...)
	s.taken = append(s.taken[:0], o.taken...)
}

// canvas is the sheet as the terminal shows it, leaving the table's box alone.
func (s *sheet) canvas(box scene.Rect) *scene.Canvas {
	c := scene.NewCanvas(s.w, s.h, box)
	plain := scene.Colors == scene.NoColor
	for y := range s.h {
		for x := range s.w {
			if box.Has(x, y) {
				continue
			}
			k := s.cells[y*s.w+x]
			switch {
			case k.cont:
				c.Spill(x, y)
			case plain && k.set:
				c.Plain(x, y, k.s)
			case plain && k.plain != "":
				c.Plain(x, y, k.plain)
			case plain:
				c.Plain(x, y, " ")
			case k.set:
				c.Set(x, y, k.fg, s.back(x, y), k.s)
			default:
				top, bottom := s.px[2*y*s.w+x], s.px[(2*y+1)*s.w+x]
				if scene.Code(top) == scene.Code(bottom) {
					c.Set(x, y, top, top, " ")
				} else {
					c.Set(x, y, top, bottom, "▀")
				}
			}
		}
	}
	return c
}
