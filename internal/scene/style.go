package scene

import "sync"

// Reset ends every style.
const Reset = "\x1b[0m"

// Code is a color as the terminal will see it, so cells that would look the same compare equal.
func Code(c RGB) int {
	r, g, b := c.Bytes()
	if Colors == ANSI256 {
		return xterm(r, g, b)
	}
	return r<<16 | g<<8 | b
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

// glyphs holds the string of each rune drawn, so cells share them.
var glyphs sync.Map

// Glyph is r as a string, shared across cells and frames.
func Glyph(r rune) string {
	if r < 0x80 {
		return ascii[r]
	}
	if s, ok := glyphs.Load(r); ok {
		return s.(string)
	}
	s, _ := glyphs.LoadOrStore(r, string(r))
	return s.(string)
}

var ascii = func() (out [0x80]string) {
	for i := range out {
		out[i] = string(rune(i))
	}
	return out
}()
