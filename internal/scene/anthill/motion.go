package anthill

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// motion paints what moves over a copy of the backdrop: mounds being flattened, each mound's weather, the
// alarm pulses, then the glyphs.
func (p *painter) motion() {
	for i := range p.l.ms {
		m := &p.l.ms[i]
		if m.dying > 0 {
			p.flatten(m)
			continue
		}
		switch m.weather {
		case scene.Clouds:
			p.dust(m)
		case scene.Storm:
			p.flood(m)
		case scene.Eruption:
			p.anteater(m)
		case scene.Summit:
			p.glow(m)
		}
		if m.needsYou {
			p.alarm(m)
		}
	}
	p.claimMounds()
	p.sprites()
}

// claimMounds marks every cell a mound and its nest stand in as its own, so a click there selects it.
func (p *painter) claimMounds() {
	for i := range p.l.ms {
		m := &p.l.ms[i]
		for y := max(0, m.entry(p.g)/2); y < min(p.p/2, m.bottom/2); y++ {
			for x := max(0, m.x0); x < min(p.w, m.x1); x++ {
				p.who[y*p.w+x] = i + 1
			}
		}
	}
}

// flatten plays a kill: the mound sinks flat, its nest and tunnels cave in from the top down, and a puff of
// dust rises and thins away.
func (p *painter) flatten(m *mound) {
	d := m.dying
	cave := smooth(.08, .95, d)
	p.burrow(m, cave)
	p.dome(m, float64(m.height)*(1-smooth(0, .6, d)), cave)
	sy := float64(p.g.sy)
	cx := float64(m.x0+m.x1) / 2
	rx := float64(m.x1-m.x0)*(.5+.6*smooth(0, .5, d)) + 2
	ry := float64(m.height)*(.4+.4*smooth(0, .5, d)) + 2
	fade := 1 - smooth(.65, 1, d)
	for y := max(0, int(sy-1.5*ry)); y < min(p.p, int(sy)+2); y++ {
		fy := float64(y) + .5
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(m.seed+5, fx*.18, fy*.22-float64(p.frame)*.4, 2)
			e := math.Hypot((fx-cx)/rx, (fy-sy)/ry)
			p.blend(x, y, mix(p.pal.dustShade, p.pal.dust, n), smooth(1, .45, e+(n-.5)*.6)*fade*.9)
		}
	}
}

// dust hangs a drifting haze of dust over a quiet mound.
func (p *painter) dust(m *mound) {
	cx := float64(m.x0+m.x1)/2 + math.Sin(float64(p.frame)*.08+float64(m.seed%7))*1.5
	cy := float64(m.entry(p.g)) - 2
	rx, ry := float64(m.x1-m.x0)*.8+3, 4.5
	for y := max(0, int(cy-ry)); y < min(p.g.sy, int(cy+ry)+1); y++ {
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx, fy := float64(x)+.5, float64(y)+.5
			n := fbm2(m.seed+11, fx*.2-float64(p.frame)*.05, fy*.3, 2)
			e := math.Hypot((fx-cx)/rx, (fy-cy)/ry)
			p.blend(x, y, mix(p.pal.dustShade, p.pal.dust, n), smooth(1, .4, e+(n-.5)*.5)*.7)
		}
	}
}

// stormCloud is the center and radii, in pixels, of the rain cloud over a flooded mound.
func (p *painter) stormCloud(m *mound) (cx, cy, rx, ry float64) {
	return float64(m.x0+m.x1) / 2, math.Max(3, float64(m.entry(p.g))-9), float64(m.x1-m.x0)*.7 + 3, 3
}

// flood darkens the sky over a mound with a rain cloud and lets water seep down its shaft into the gallery,
// rising and falling.
func (p *painter) flood(m *mound) {
	cx, cy, rx, ry := p.stormCloud(m)
	for y := max(0, int(cy-ry)); y < min(p.g.sy, int(cy+ry)+1); y++ {
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx, fy := float64(x)+.5, float64(y)+.5
			n := fbm2(m.seed+13, fx*.25, fy*.4, 2)
			e := math.Hypot((fx-cx)/rx, (fy-cy)/ry)
			p.blend(x, y, mix(p.pal.storm, p.pal.stormTop, smooth(cy+ry, cy-ry, fy)*n), smooth(1, .5, e+(n-.5)*.5))
		}
	}
	sy := p.g.sy
	level := 3 + int(math.Abs(math.Sin(float64(p.frame)*.2))*float64(max(2, (m.bottom-sy)/3)))
	for y := max(sy, m.bottom-level); y <= m.bottom; y++ {
		p.set(m.cx, y, p.pal.water)
	}
	if m.parent >= 0 {
		c := p.l.cs[m.parent]
		gy := p.g.gallery(c.depth)
		for x := max(c.gx0, m.x0-2); x <= min(c.gx1, m.x1+1); x++ {
			p.set(x, gy+1, p.pal.water)
			if (x+p.frame/2)%5 == 0 {
				p.set(x, gy, p.pal.waterLit)
			}
		}
	}
	for k := range 3 {
		y := sy + (p.frame+k*5)%max(1, m.bottom-sy-level)
		p.set(m.cx, y, p.pal.waterLit)
	}
	for _, c := range m.chambers {
		if c.state != scene.Merged {
			p.set(m.cx+3*c.side-1, c.y+1, p.pal.water)
			p.set(m.cx+3*c.side, c.y+1, p.pal.water)
		}
	}
}

// anteater stands beside the mound with its snout on the entrance, its tongue raiding down the shaft.
func (p *painter) anteater(m *mound) {
	sy := float64(p.g.sy)
	side := -1.0
	if m.x0 < 14 {
		side = 1
	}
	edge := float64(m.x0)
	if side > 0 {
		edge = float64(m.x1)
	}
	bx, by := edge+side*7, sy-3.5
	for y := int(by - 4); y < int(sy); y++ {
		for x := int(bx - 7); x <= int(bx+7); x++ {
			fx, fy := float64(x)+.5, float64(y)+.5
			e := math.Hypot((fx-bx)/5.5, (fy-by)/2.6)
			tail := math.Hypot((fx-(bx-side*6))/2.5, (fy-(by-1.5))/2.8)
			switch {
			case e < 1:
				c := p.pal.anteater
				if s := (fx - bx) * side; math.Abs(fy-by+s*.45) < .7 && s > -3 {
					c = p.pal.stripe
				}
				p.set(x, y, c)
			case tail < 1:
				p.set(x, y, mix(p.pal.anteater, p.pal.dustShade, .4))
			}
		}
	}
	for _, lx := range [2]float64{bx - 3, bx + 3} {
		for y := int(by + 2); y < int(sy); y++ {
			p.set(int(lx), y, p.pal.anteater)
		}
	}
	hx, hy := bx+side*5, by-1
	ex, ey := float64(m.cx)+.5, float64(m.entry(p.g))
	steps := int(math.Hypot(ex-hx, ey-hy)) + 1
	for k := range steps {
		t := float64(k) / float64(steps)
		p.set(int(lerp(hx, ex, t)), int(lerp(hy, ey, t)), mix(p.pal.anteater, p.pal.stripe, .15))
	}
	reach := (math.Sin(float64(p.frame)*.6) + 1) / 2
	tip := int(ey) + int(reach*float64(m.bottom-int(ey)))
	for y := int(ey); y <= tip; y++ {
		p.set(m.cx, y, p.pal.tongue)
	}
}

// glow warms the sky round a full mound.
func (p *painter) glow(m *mound) {
	cx, cy := float64(m.x0+m.x1)/2, float64(m.entry(p.g))+1
	r := float64(m.x1-m.x0) + 4
	for y := max(0, int(cy-r)); y < p.g.sy-1; y++ {
		for x := max(0, int(cx-r*1.3)); x < min(p.w, int(cx+r*1.3)+1); x++ {
			if p.solid[y*p.w+x] {
				continue
			}
			d := math.Hypot((float64(x)+.5-cx)/1.3, float64(y)+.5-cy)
			p.blend(x, y, p.pal.glow, .4*smooth(r, 0, d))
		}
	}
}

// alarm pulses red rings out from the entrance of a mound that needs its owner.
func (p *painter) alarm(m *mound) {
	ex, ey := float64(m.cx)+.5, float64(m.entry(p.g))-1.5
	r := 1 + float64(p.frame%4)*1.6
	for y := int(ey - 8); y <= int(ey+8); y++ {
		for x := int(ex - 8); x <= int(ex+8); x++ {
			d := math.Hypot(float64(x)+.5-ex, float64(y)+.5-ey)
			p.blend(x, y, p.pal.red, .6*math.Exp(-(d-r)*(d-r)/1.2)*(1-r/9))
		}
	}
}
