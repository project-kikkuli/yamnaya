package anthill

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// backdrop paints everything that holds still: sky, sun or moon, soil and its roots and pebbles, the colony
// galleries, and every mound not being flattened with its nest, shaft and chambers.
func (p *painter) backdrop() {
	p.sky()
	p.soil()
	p.roots()
	for k := range p.l.cs {
		p.gallery(&p.l.cs[k])
	}
	for i := range p.l.ms {
		if m := &p.l.ms[i]; m.dying == 0 {
			p.burrow(m, 0)
		}
	}
	p.grass()
	for i := range p.l.ms {
		if m := &p.l.ms[i]; m.dying == 0 {
			p.dome(m, float64(m.height), 0)
		}
	}
}

func (p *painter) sky() {
	sy := p.g.sy
	for y := range sy {
		c := p.pal.skyAt(float64(y) / float64(sy))
		for x := range p.w {
			p.px[y*p.w+x] = c
		}
	}
	if p.pal.stars > 0 {
		for y := range sy * 3 / 4 {
			for x := range p.w {
				if i := y*p.w + x; spread(0x57a, i)%1000 < 7 {
					p.px[i] = mix(p.px[i], white, p.pal.stars*(.4+.6*unit(0x57b, i)))
				}
			}
		}
	}
	// The sun crosses from 6 to 19.5, the moon through the night, each on a low arc.
	t := (p.hour - 6) / 13.5
	if p.hour < 6 || p.hour > 19.5 {
		t = math.Mod(p.hour-19.5+24, 24) / 10.5
	}
	cx := float64(p.w) * (.08 + .84*t)
	cy := float64(sy) * (.8 - .6*math.Sin(math.Pi*t))
	r := math.Max(2, float64(sy)*.07)
	for y := max(0, int(cy-3*r)); y < min(sy, int(cy+3*r)+1); y++ {
		for x := max(0, int(cx-3*r)); x < min(p.w, int(cx+3*r)+1); x++ {
			d := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy)
			if d < r {
				p.px[y*p.w+x] = p.pal.sun
				continue
			}
			p.blend(x, y, p.pal.sun, .35*smooth(3*r, r, d)*(.4+.6*p.pal.day))
		}
	}
}

// soil lays the layers under the surface: a dark humus skin, wavy bands of earth, then deep clay, flecked with
// pebbles.
func (p *painter) soil() {
	sy := p.g.sy
	band := float64(max(3, (p.p-sy)/5))
	for x := range p.w {
		wobble := (fbm(0x501, float64(x)*.06, 3) - .5) * band * .9
		for y := sy; y < p.p; y++ {
			var c rgb
			switch d := float64(y-sy) + wobble; {
			case y < sy+2:
				c = p.pal.humus
			case d >= band*4:
				c = p.pal.deep
			default:
				c = p.pal.soil[int(math.Max(0, d)/band)%4]
			}
			grain := fbm2(0x502, float64(x)*.35, float64(y)*.5, 2)
			c = mix(c, p.pal.deep, (grain-.45)*.5)
			if spread(0x503, y*p.w+x)%1000 < 5 && y > sy+2 {
				c = mix(p.pal.pebble, c, .3*unit(0x504, x))
				p.fill(x+1, y, mix(p.pal.pebble, c, .5))
			}
			p.fill(x, y, c)
		}
	}
}

// roots wind down from the grass between mounds.
func (p *painter) roots() {
	for k := range max(2, p.w/9) {
		x := int(unit(0x600, k) * float64(p.w))
		if p.underMound(x) {
			continue
		}
		fx := float64(x)
		n := 5 + int(unit(0x601, k)*float64(p.g.nest))
		for d := range n {
			y := p.g.sy + d
			fx += (unit(0x602, k*97+d) - .5) * .9
			p.fill(int(fx), y, mix(p.pal.root, p.pal.humus, float64(d)/float64(n)*.6))
			if spread(0x603, k*31+d)%7 == 0 {
				p.fill(int(fx)+1-2*int(spread(0x604, k+d)%2), y+1, mix(p.pal.root, p.pal.humus, .5))
			}
		}
	}
}

func (p *painter) underMound(x int) bool {
	for i := range p.l.ms {
		if m := &p.l.ms[i]; x >= m.x0-1 && x <= m.x1 {
			return true
		}
	}
	return false
}

func (p *painter) grass() {
	sy := p.g.sy
	for x := range p.w {
		c := mix(p.pal.grass, p.pal.humus, unit(0x700, x)*.3)
		p.fill(x, sy-1, c)
		if spread(0x701, x)%4 == 0 {
			p.fill(x, sy-2, mix(c, p.pal.skyAt(1), .35))
		}
	}
}

// gallery digs a colony's tunnel under its mounds, and the link up from a nested colony to its parent's.
func (p *painter) gallery(c *colony) {
	gy := p.g.gallery(c.depth)
	for x := c.gx0; x <= c.gx1; x++ {
		p.hollow(x, gy, p.pal.comb)
		p.hollow(x, gy+1, p.pal.cavity)
	}
	if c.parent >= 0 {
		top := p.g.gallery(p.l.cs[c.parent].depth)
		for y := top; y < gy; y++ {
			p.hollow(c.link, y, p.pal.cavity)
		}
	}
}

// open digs a tunnel pixel, or once cave is above 0, leaves it filled with rubble where the collapse reached.
func (p *painter) open(m *mound, x, y int, c rgb, cave float64) {
	if cave > 0 && unit(m.seed^0xca5e, y*p.w+x) < cave {
		p.fill(x, y, mix(p.pal.rubble, p.pal.soil[1], unit(m.seed, y*p.w+x)*.6))
		return
	}
	p.hollow(x, y, c)
}

// burrow digs a mound's nest, its shaft down to the colony gallery and its chambers, caved in as far as cave.
func (p *painter) burrow(m *mound, cave float64) {
	sy := p.g.sy
	ncx, ncy, rx, ry := m.nest(p.g)
	for y := sy + 1; y < int(ncy+ry)+1; y++ {
		for x := int(ncx - rx); x <= int(ncx+rx); x++ {
			dx, dy := (float64(x)+.5-ncx)/rx, (float64(y)+.5-ncy)/ry
			if dx*dx+dy*dy >= 1 {
				continue
			}
			if wall := (x+(y/2)%2*2)%3 == 0 || y%2 == 1 && (x+y)%2 == 0; !wall {
				p.open(m, x, y, mix(p.pal.comb, p.pal.cavity, unit(m.seed, x*7+y)*.6), cave)
			}
		}
	}
	for y := sy; y <= m.bottom; y++ {
		p.open(m, m.cx, y, p.pal.cavity, cave)
	}
	for _, c := range m.chambers {
		p.chamber(m, c, cave)
	}
}

// chamber hollows a pull request's chamber beside the shaft: stocked and glowing green, half fallen in, or
// filled with gold food and sealed off from the shaft once merged.
func (p *painter) chamber(m *mound, c chamber, cave float64) {
	mid := m.cx + 3*c.side
	for x := mid - 1; x <= mid+1; x++ {
		for y := c.y; y <= c.y+1; y++ {
			col := p.pal.cavity
			switch c.state {
			case scene.Green:
				col = mix(p.pal.cavity, p.pal.green, .45)
			case scene.Failing:
				if (x+y)%2 == 0 || y == c.y {
					p.fill(x, y, mix(p.pal.rubble, p.pal.red, .25))
					continue
				}
			case scene.Merged:
				col = mix(p.pal.gold, p.pal.comb, .3*float64(y-c.y))
			}
			p.open(m, x, y, col, cave)
		}
	}
	for x := m.cx + c.side; x != mid-c.side; x += c.side {
		p.open(m, x, c.y+1, p.pal.cavity, cave)
	}
	if c.state == scene.Merged {
		p.fill(mid-c.side, c.y, p.pal.wall)
		p.fill(mid-c.side, c.y+1, p.pal.wall)
	}
}

// dome raises the mound height pixels over the surface, lit from the west, with its shaft opening on top; a
// collapse fills the shaft as it goes.
func (p *painter) dome(m *mound, height float64, cave float64) {
	sy := p.g.sy
	for x := m.x0; x < m.x1; x++ {
		top := m.top(x, height, p.g)
		for y := top; y < sy; y++ {
			light := 1 - float64(x-m.x0)/float64(max(1, m.x1-m.x0))
			c := mix(p.pal.mound, p.pal.moundLit, light*.7)
			if y == top {
				c = mix(c, p.pal.moundLit, .4)
			}
			grain := unit(m.seed+1, y*p.w+x)
			c = mix(c, p.pal.wall, grain*.25)
			p.fill(x, y, c)
		}
	}
	if height < 1 || cave > .5 {
		return
	}
	for y := sy - int(math.Round(height)); y < sy; y++ {
		p.hollow(m.cx, y, p.pal.wall)
	}
}
