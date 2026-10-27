package skyline

import "github.com/project-kikkuli/yamnaya/internal/scene"

// facade paints a building floor by floor, each floor a cell: windows in its upper pixel, the slab under them.
// Floors not yet built are a steel frame. Every row is moved down drop pixels and cut off at the base, which
// is how a demolished building sinks.
func (p *painter) facade(b *building, drop int) {
	done := b.weather == scene.Summit
	share := .65 * p.pal.lights
	if done {
		share = 1
	}
	lit := p.pal.lit
	if done {
		lit = mix(lit, p.pal.gold, .3)
	}
	edgeLit, edgeDark := mix(p.pal.facade, p.pal.label, .08), p.pal.facade.Scale(.78)
	steel := p.pal.steel
	mid := b.cx()
	for f := range b.planned {
		win, slab := b.base-2*f-2+drop, b.base-2*f-1+drop
		if win >= b.base {
			continue
		}
		pr := b.floors[f]
		for x := b.x0; x < b.x1; x++ {
			edge := x == b.x0 || x == b.x1-1
			if f >= b.built {
				column := edge || (b.x1-b.x0 >= 5 && x == mid)
				if column {
					p.fill(x, win, steel)
				}
				if pr != 0 {
					p.fill(x, win, p.floorColor(pr, p.pal.glass))
				}
				if slab < b.base {
					p.fill(x, slab, steel)
				}
				continue
			}
			pier := edge || b.style == 1 && (x-b.x0)%2 == 0
			c := p.pal.facade
			switch {
			case x == b.x0:
				c = edgeLit
			case x == b.x1-1:
				c = edgeDark
			case pier:
			case unit(b.seed, f*97+x-b.x0) < share || f == 0 && p.pal.lights > .3:
				c = mix(lit, white, .25*unit(b.seed+1, f*97+x))
			default:
				c = mix(p.pal.glass, p.pal.skyAt(float64(win)/float64(p.hz)), .3)
			}
			if pr != 0 && !edge {
				c = p.floorColor(pr, c)
			}
			p.fill(x, win, c)
			if slab < b.base {
				s := p.pal.slab
				if pr == int8(scene.Failing)+1 {
					s = p.pal.red.Scale(.38)
				} else if pr == int8(scene.Merged)+1 {
					s = p.pal.gold.Scale(.7)
				}
				p.fill(x, slab, s)
			}
		}
	}
}

// floorColor is a window on a pull request's floor: grey fit-out while open, lit green when CI passes, dark
// red when it fails, gold glass once merged.
func (p *painter) floorColor(pr int8, c rgb) rgb {
	switch scene.CampState(pr - 1) {
	case scene.Green:
		return p.pal.green
	case scene.Failing:
		return p.pal.red.Scale(.62)
	case scene.Merged:
		return mix(p.pal.gold, white, .25)
	}
	return mix(c, p.pal.steel, .7)
}

// isWindow reports whether column x of a building holds windows rather than a pier.
func (b *building) isWindow(x int) bool {
	return x > b.x0 && x < b.x1-1 && !(b.style == 1 && (x-b.x0)%2 == 0)
}
