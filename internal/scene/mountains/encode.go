package mountains

import "github.com/project-kikkuli/yamnaya/internal/scene"

// paintCell puts cell x, y on c: its glyph, or the two pixels it holds as a half block.
func (p *painter) paintCell(c *scene.Canvas, x, y int) {
	top, bottom := p.px[2*y*p.w+x], p.px[(2*y+1)*p.w+x]
	g := p.cells[y*p.w+x]
	glyph := g.s
	if glyph == "" {
		glyph = scene.Glyph(g.r)
	}
	if scene.Colors == scene.NoColor {
		if g.set {
			c.Plain(x, y, glyph)
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
		c.Set(x, y, g.fg, g.bg, glyph)
	case scene.Code(top) == scene.Code(bottom):
		c.Set(x, y, top, top, " ")
	default:
		c.Set(x, y, top, bottom, "▀")
	}
}
