package scene

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Canvas is a scene around a table, cell by cell: what the terminal shows, or is to show.
type Canvas struct {
	w, h int
	box  Rect
	// ink and glyph of each cell; a cell a wide glyph spills into has the glyph "".
	ink   []ink
	glyph []string
}

// ink is a cell's colors as the terminal sees them: plain, reset, or a foreground and background code.
type ink uint64

const (
	inkReset ink = iota
	inkPlain
	inkColor ink = 1 << 63
)

func inkOf(fg, bg RGB) ink { return inkColor | ink(Code(fg))<<24 | ink(Code(bg)) }

// Blank is a canvas of empty cells, where a scene rises from nothing.
func Blank(width, height int) *Canvas {
	c := &Canvas{w: width, h: height, ink: make([]ink, width*height), glyph: make([]string, width*height)}
	for i := range c.glyph {
		c.glyph[i] = " "
	}
	return c
}

// NewCanvas is a blank canvas a theme paints, with the table to float over box; cells under box are never shown.
func NewCanvas(width, height int, box Rect) *Canvas {
	c := Blank(width, height)
	c.box = box
	return c
}

// Set draws glyph in fg on bg at x, y. A wide glyph takes the next cell too, which Spill marks.
func (c *Canvas) Set(x, y int, fg, bg RGB, glyph string) {
	if x >= 0 && y >= 0 && x < c.w && y < c.h {
		c.ink[y*c.w+x], c.glyph[y*c.w+x] = inkOf(fg, bg), glyph
	}
}

// Plain draws glyph at x, y in the terminal's own colors, which is all there is under NoColor.
func (c *Canvas) Plain(x, y int, glyph string) {
	if x >= 0 && y >= 0 && x < c.w && y < c.h {
		c.ink[y*c.w+x], c.glyph[y*c.w+x] = inkPlain, glyph
	}
}

// Spill marks x, y as the second cell of the wide glyph Set just before it.
func (c *Canvas) Spill(x, y int) {
	if x >= 0 && y >= 0 && x < c.w && y < c.h {
		c.glyph[y*c.w+x] = ""
	}
}

// Cell is the escape and glyph drawn at x, y.
func (c *Canvas) Cell(x, y int) (string, string) {
	return string(c.ink[y*c.w+x].append(nil)), c.glyph[y*c.w+x]
}

// Box is where the table floats on the canvas.
func (c *Canvas) Box() Rect { return c.box }

// Fits reports whether the canvas is the size of the terminal.
func (c *Canvas) Fits(width, height int) bool { return c != nil && c.w == width && c.h == height }

// Toward is c with at most n of the cells that differ from goal changed to match it, bottom row first, so the
// scene rises; cells the goal's table covers change without counting. It also reports how many still differ.
func (c *Canvas) Toward(goal *Canvas, n int) (*Canvas, int) {
	if !c.Fits(goal.w, goal.h) {
		c = Blank(goal.w, goal.h)
	}
	out := &Canvas{w: c.w, h: c.h, box: goal.box, ink: append([]ink(nil), c.ink...), glyph: append([]string(nil), c.glyph...)}
	left := 0
	for y := c.h - 1; y >= 0; y-- {
		for x := 0; x < c.w; x++ {
			i := y*c.w + x
			if out.ink[i] == goal.ink[i] && out.glyph[i] == goal.glyph[i] {
				continue
			}
			if goal.box.Has(x, y) {
				out.ink[i], out.glyph[i] = goal.ink[i], goal.glyph[i]
				continue
			}
			if n <= 0 {
				left++
				continue
			}
			n--
			// A wide glyph and the cell it spills into change together, or the row would shift.
			for {
				out.ink[i], out.glyph[i] = goal.ink[i], goal.glyph[i]
				if x+1 >= c.w || (goal.glyph[i+1] != "" && out.glyph[i+1] != "") {
					break
				}
				x, i = x+1, i+1
			}
		}
	}
	return out, left
}

// Draw is the canvas with the table floating on it.
func (c *Canvas) Draw(table string) string {
	lines, tw, box := place(table, c.w, c.h)
	var b strings.Builder
	b.Grow(c.w * c.h * 12)
	buf := make([]byte, 0, 48)
	for y := range c.h {
		last := inkPlain
		for x := 0; x < c.w; x++ {
			if box.Has(x, y) {
				line := lines[y-box.Y0]
				left := (box.X1 - box.X0 - tw) / 2
				b.WriteString(Reset)
				b.WriteString(strings.Repeat(" ", left))
				b.WriteString(line)
				b.WriteString(Reset)
				b.WriteString(strings.Repeat(" ", box.X1-box.X0-left-ansi.StringWidth(line)))
				x, last = box.X1-1, inkPlain
				continue
			}
			i := y*c.w + x
			if c.glyph[i] == "" {
				continue
			}
			if c.ink[i] != last {
				buf = c.ink[i].append(buf[:0])
				b.Write(buf)
				last = c.ink[i]
			}
			b.WriteString(c.glyph[i])
		}
		b.WriteString(Reset)
		if y < c.h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (k ink) append(buf []byte) []byte {
	switch k {
	case inkReset:
		return append(buf, Reset...)
	case inkPlain:
		return buf
	}
	buf = append(buf, "\x1b["...)
	buf = appendCode(buf, 38, int(k>>24&0xffffff))
	buf = append(buf, ';')
	buf = appendCode(buf, 48, int(k&0xffffff))
	return append(buf, 'm')
}

func appendCode(buf []byte, base, code int) []byte {
	buf = strconv.AppendInt(buf, int64(base), 10)
	if Colors == ANSI256 {
		buf = append(buf, ";5;"...)
		return strconv.AppendInt(buf, int64(code), 10)
	}
	buf = append(buf, ";2;"...)
	buf = strconv.AppendInt(buf, int64(code>>16), 10)
	buf = append(buf, ';')
	buf = strconv.AppendInt(buf, int64(code>>8&255), 10)
	buf = append(buf, ';')
	return strconv.AppendInt(buf, int64(code&255), 10)
}

// Rect is the cells x0 <= x < x1, y0 <= y < y1.
type Rect struct{ X0, Y0, X1, Y1 int }

func (r Rect) Has(x, y int) bool { return x >= r.X0 && x < r.X1 && y >= r.Y0 && y < r.Y1 }

// Ink is the foreground and background drawn at x, y under TrueColor; colored is false for a plain or reset cell.
func (c *Canvas) Ink(x, y int) (fg, bg RGB, colored bool) {
	k := c.ink[y*c.w+x]
	if k&inkColor == 0 {
		return RGB{}, RGB{}, false
	}
	return codeRGB(int(k >> 24 & 0xffffff)), codeRGB(int(k & 0xffffff)), true
}

func codeRGB(code int) RGB {
	return RGB{float64(code>>16) / 255, float64(code>>8&255) / 255, float64(code&255) / 255}
}
