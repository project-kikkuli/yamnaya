package scene

import (
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const reset = "\x1b[0m"

// encode turns pixels into half-block cells, two pixels a cell, and splices the table rows in.
func (p *painter) encode(lines []string, tw int) string {
	var b strings.Builder
	b.Grow(p.w * p.p * 12)
	rows := p.p / 2
	for y := range rows {
		var last string
		for x := 0; x < p.w; x++ {
			if p.table.has(x, y) {
				line := lines[y-p.table.y0]
				left := (p.table.x1 - p.table.x0 - tw) / 2
				b.WriteString(reset)
				b.WriteString(strings.Repeat(" ", left))
				b.WriteString(line)
				b.WriteString(reset)
				b.WriteString(strings.Repeat(" ", p.table.x1-p.table.x0-left-ansi.StringWidth(line)))
				x, last = p.table.x1-1, ""
				continue
			}
			style, r := p.cell(x, y)
			if style != last {
				b.WriteString(style)
				last = style
			}
			b.WriteRune(r)
		}
		b.WriteString(reset)
		if y < rows-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (p *painter) cell(x, y int) (string, rune) {
	top, bottom := p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x]
	g := p.cells[y*p.w+x]
	if profile == noColor {
		if g.set {
			return "", g.r
		}
		up, down := p.solid[2*y*p.w+x], p.solid[(2*y+1)*p.w+x]
		switch {
		case up && down:
			return "", '█'
		case up:
			return "", '▀'
		case down:
			return "", '▄'
		}
		return "", ' '
	}
	if g.set {
		return sgr(g.fg, mix(top, bottom, .5)), g.r
	}
	if code(top) == code(bottom) {
		return sgr(top, top), ' '
	}
	return sgr(top, bottom), '▀'
}

// code is a color as the terminal will see it, so cells that would look the same compare equal.
func code(c rgb) int {
	r, g, b := c.bytes()
	if profile == ansi256 {
		return xterm(r, g, b)
	}
	return r<<16 | g<<8 | b
}

func sgr(fg, bg rgb) string {
	buf := make([]byte, 0, 40)
	buf = append(buf, "\x1b["...)
	buf = appendColor(buf, 38, fg)
	buf = append(buf, ';')
	buf = appendColor(buf, 48, bg)
	buf = append(buf, 'm')
	return string(buf)
}

func appendColor(buf []byte, base int, c rgb) []byte {
	buf = strconv.AppendInt(buf, int64(base), 10)
	if profile == ansi256 {
		r, g, b := c.bytes()
		buf = append(buf, ";5;"...)
		return strconv.AppendInt(buf, int64(xterm(r, g, b)), 10)
	}
	r, g, b := c.bytes()
	buf = append(buf, ";2;"...)
	buf = strconv.AppendInt(buf, int64(r), 10)
	buf = append(buf, ';')
	buf = strconv.AppendInt(buf, int64(g), 10)
	buf = append(buf, ';')
	return strconv.AppendInt(buf, int64(b), 10)
}

// xterm is the nearest of the 256 palette colors, from the 6x6x6 cube or the gray ramp.
func xterm(r, g, b int) int {
	level := func(v int) int {
		if v < 48 {
			return 0
		}
		return min(5, (v-35)/40)
	}
	value := func(l int) int {
		if l == 0 {
			return 0
		}
		return 55 + l*40
	}
	cr, cg, cb := level(r), level(g), level(b)
	cube := 16 + 36*cr + 6*cg + cb
	gray := min(23, max(0, (r+g+b)/3-3)/10)
	dist := func(x, y, z int) int { return (x-r)*(x-r) + (y-g)*(y-g) + (z-b)*(z-b) }
	gv := 8 + gray*10
	if dist(gv, gv, gv) < dist(value(cr), value(cg), value(cb)) {
		return 232 + gray
	}
	return cube
}
