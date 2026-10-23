package scene

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Canvas is the mountains around a table, cell by cell: what the terminal shows, or is to show.
type Canvas struct {
	w, h int
	box  rect
	// style and glyph of each cell; a cell a wide glyph spills into has the glyph "".
	style, glyph []string
}

// Blank is a canvas of empty cells, where mountains rise from nothing.
func Blank(width, height int) *Canvas {
	c := &Canvas{w: width, h: height, style: make([]string, width*height), glyph: make([]string, width*height)}
	for i := range c.style {
		c.style[i], c.glyph[i] = reset, " "
	}
	return c
}

// Fits reports whether the canvas is the size of the terminal.
func (c *Canvas) Fits(width, height int) bool { return c != nil && c.w == width && c.h == height }

func (p *painter) canvas() *Canvas {
	c := Blank(p.w, p.p/2)
	c.box = p.table
	for y := range c.h {
		for x := range c.w {
			i := y*c.w + x
			g := p.cells[i]
			if p.table.has(x, y) {
				continue
			}
			if g.cont {
				c.glyph[i] = ""
				continue
			}
			style, r := p.cell(x, y)
			c.style[i] = style
			if g.s != "" {
				c.glyph[i] = g.s
			} else {
				c.glyph[i] = string(r)
			}
		}
	}
	return c
}

// Toward is c with at most n of the cells that differ from goal changed to match it, bottom row first, so the
// mountains rise; cells the goal's table covers change without counting. It also reports how many still differ.
func (c *Canvas) Toward(goal *Canvas, n int) (*Canvas, int) {
	if !c.Fits(goal.w, goal.h) {
		c = Blank(goal.w, goal.h)
	}
	out := &Canvas{w: c.w, h: c.h, box: goal.box, style: append([]string(nil), c.style...), glyph: append([]string(nil), c.glyph...)}
	left := 0
	for y := c.h - 1; y >= 0; y-- {
		for x := 0; x < c.w; x++ {
			i := y*c.w + x
			if out.style[i] == goal.style[i] && out.glyph[i] == goal.glyph[i] {
				continue
			}
			if goal.box.has(x, y) {
				out.style[i], out.glyph[i] = goal.style[i], goal.glyph[i]
				continue
			}
			if n <= 0 {
				left++
				continue
			}
			n--
			// A wide glyph and the cell it spills into change together, or the row would shift.
			for {
				out.style[i], out.glyph[i] = goal.style[i], goal.glyph[i]
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
	for y := range c.h {
		last := ""
		for x := 0; x < c.w; x++ {
			if box.has(x, y) {
				line := lines[y-box.y0]
				left := (box.x1 - box.x0 - tw) / 2
				b.WriteString(reset)
				b.WriteString(strings.Repeat(" ", left))
				b.WriteString(line)
				b.WriteString(reset)
				b.WriteString(strings.Repeat(" ", box.x1-box.x0-left-ansi.StringWidth(line)))
				x, last = box.x1-1, ""
				continue
			}
			i := y*c.w + x
			if c.glyph[i] == "" {
				continue
			}
			if c.style[i] != last {
				b.WriteString(c.style[i])
				last = c.style[i]
			}
			b.WriteString(c.glyph[i])
		}
		b.WriteString(reset)
		if y < c.h-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
