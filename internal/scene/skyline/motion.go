package skyline

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// motion paints what moves over a copy of the backdrop: the city's own life, weather that strikes, kaiju,
// demolitions, the selection's searchlights and the beacons, then the glyphs.
func (p *painter) motion() {
	p.ambient()
	for i := range p.l.bs {
		b := &p.l.bs[i]
		if b.dying > 0 {
			p.demolish(b)
			continue
		}
		switch b.weather {
		case scene.Storm:
			p.bolt(b)
		case scene.Eruption:
			p.fire(b)
			p.kaiju(i)
		}
		if b.selected {
			p.searchlights(b)
		}
		if b.needsYou {
			p.beaconGlow(b)
		}
	}
	p.claimBuildings()
	p.sprites()
}

// claimBuildings marks every cell a building stands in as its own, so a click there selects it.
func (p *painter) claimBuildings() {
	for i := range p.l.bs {
		b := &p.l.bs[i]
		top := (b.base - 2*b.planned + b.drop()) / 2
		for y := max(0, top); y < b.base/2; y++ {
			for x := max(0, b.x0); x < min(p.w, b.x1); x++ {
				p.who[y*p.w+x] = i + 1
			}
		}
	}
}

// ambient takes turns: stars twinkle and windows go on and off on one beat, cars drive and the river glints.
func (p *painter) ambient() {
	beat := p.frame / int(scene.Ambient/scene.Step)
	for _, s := range p.stars {
		b := s.b * (.45 + .55*unit(0x57a7, s.i*31+beat))
		p.px[s.i] = mix(p.px[s.i], white, b*p.pal.stars)
	}
	if p.pal.lights > .25 {
		p.flicker(beat)
	}
	p.cars()
	p.glints(beat)
}

// flicker turns a few windows of each building on or off, as people come and go; pull request floors keep.
func (p *painter) flicker(beat int) {
	for i := range p.l.bs {
		b := &p.l.bs[i]
		if b.dying > 0 || b.weather == scene.Summit || b.x1-b.x0 < 3 {
			continue
		}
		for k := range 3 {
			f := int(spread(b.seed, beat*3+k) % uint64(b.built))
			x := b.x0 + 1 + int(spread(b.seed^1, beat*3+k)%uint64(b.x1-b.x0-2))
			if b.floors[f] != 0 || !b.isWindow(x) {
				continue
			}
			y := b.base - 2*f - 2
			if p.px[y*p.w+x].Luma() > .5 {
				p.set(x, y, mix(p.pal.glass, p.pal.skyAt(float64(y)/float64(p.hz)), .3))
			} else {
				p.set(x, y, p.pal.lit)
			}
		}
	}
}

func (p *painter) cars() {
	n := max(2, p.w/24)
	body := mix(p.pal.slab, p.pal.label, .3)
	for k := range n {
		dir, row := 1, p.hz+5
		if k%2 == 0 {
			dir, row = -1, p.hz+3
		}
		span := float64(p.w + 8)
		x := math.Mod(unit(0xca8, k)*span+float64(dir*p.frame)*(1+unit(0xca7, k)*1.5), span)
		if x < 0 {
			x += span
		}
		xi := int(x) - 4
		c := mix(body, p.pal.facade, unit(0xca9, k))
		for j := range 3 {
			p.set(xi+j, row, c)
		}
		front, back := xi+3, xi-1
		if dir < 0 {
			front, back = xi-1, xi+3
		}
		if p.pal.lights > .2 {
			p.set(front, row, p.pal.lamp)
			p.blend(front+dir, row, p.pal.lamp, .45*p.pal.lights)
			p.set(back, row, p.pal.tail)
		}
	}
}

func (p *painter) glints(beat int) {
	glint := mix(p.pal.skyAt(.97), p.pal.lit, p.pal.lights*.5)
	for y := p.hz + 7; y < p.p; y++ {
		for x := range p.w {
			i := y*p.w + x
			if spread(0x91e, i*7+beat)%100 < 2 {
				p.px[i] = mix(p.px[i], glint, .5)
			}
		}
	}
}

// bolt strikes now and then from a storm's cloud down onto the roof.
func (p *painter) bolt(b *building) {
	if (p.frame/4+int(b.seed%97))%5 != 0 || p.frame%4 > 1 {
		return
	}
	cx, cy, _, ry := b.stormShape()
	x, roof := int(cx)+int(spread(b.seed, p.frame/4)%3)-1, b.base-2*b.built
	for y := int(cy + ry*.6); y < roof; y++ {
		p.set(x, y, p.pal.bolt)
		p.blend(x-1, y, p.pal.bolt, .35)
		p.blend(x+1, y, p.pal.bolt, .35)
		if spread(b.seed+5, y*13+p.frame)%3 == 0 {
			x += int(spread(b.seed+6, y+p.frame)%3) - 1
		}
	}
}

// fire burns in the top floors of a building under kaiju attack.
func (p *painter) fire(b *building) {
	for f := max(0, b.built-3); f < b.built; f++ {
		for x := b.x0; x < b.x1; x++ {
			y := b.base - 2*f - 2
			switch r := unit(b.seed+9, (f*31+x)*977+p.frame); {
			case r < .25:
				p.set(x, y, p.pal.fireHot)
			case r < .55:
				p.set(x, y, p.pal.fire)
			}
			if f == b.built-1 && unit(b.seed+10, x*31+p.frame) < .5 {
				p.blend(x, y-1, p.pal.fire, .7)
			}
		}
	}
}

// demolish implodes a killed building: charges flash floor by floor, then it sinks into its own dust cloud,
// which thins away as the kill ends.
func (p *painter) demolish(b *building) {
	d := b.dying
	drop := b.drop()
	p.facade(b, drop)
	if d < .25 {
		for f := range b.built {
			if unit(b.seed+3, f*13+int(d*60)) < .4 {
				x, y := b.x0+int(spread(b.seed, f)%uint64(b.x1-b.x0)), b.base-2*f-2+drop
				p.set(x, y, p.pal.fireHot)
				p.blend(x-1, y, p.pal.fire, .6)
				p.blend(x+1, y, p.pal.fire, .6)
			}
		}
	}
	cx := float64(b.x0+b.x1) / 2
	rise := smooth(.03, .6, d)
	rx := float64(b.x1-b.x0)*(.7+1.3*rise) + 2
	ry := float64(2*b.planned)*(.2+.55*smooth(.05, .7, d)) + 3
	fade := 1 - smooth(.7, 1, d)
	for y := max(0, int(float64(b.base)-ry)); y < min(p.p, b.base+4); y++ {
		fy := float64(y) + .5
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(b.seed+5, fx*.16, fy*.2-float64(p.frame)*.5, 2)
			e := math.Hypot((fx-cx)/rx, (fy-float64(b.base))/ry)
			a := smooth(1, .5, e+(n-.5)*.7) * fade * .95
			p.blend(x, y, mix(p.pal.dustShade, p.pal.dust, n), a)
		}
	}
}

// searchlights cross two beams from the street over the selected building.
func (p *painter) searchlights(b *building) {
	cx := float64(b.x0+b.x1) / 2
	ty := float64(max(1, b.base-2*b.planned-8))
	for _, side := range [2]float64{-1, 1} {
		sx := cx + side*(float64(b.x1-b.x0)+4)
		for y := int(ty); y < p.hz; y++ {
			t := (float64(y) - ty) / (float64(p.hz) - ty)
			mx, half := lerp(cx-side*2, sx, t), .7+1.3*t
			for x := int(mx - half - 1); x <= int(mx+half+1); x++ {
				a := smooth(half+.8, half-.4, math.Abs(float64(x)+.5-mx)) * (.28 - .12*t)
				p.blend(x, y, p.pal.rim, a)
			}
		}
	}
}

// beaconGlow is the red light the needs-you beacon throws round itself while it is lit.
func (p *painter) beaconGlow(b *building) {
	if p.frame/4%2 != 0 {
		return
	}
	x, row := p.beaconAt(b)
	y := 2*row + 1
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			p.blend(x+dx, y+dy, p.pal.red, .45*math.Exp(-float64(dx*dx+dy*dy)/3))
		}
	}
}

// beaconAt is the cell the needs-you beacon blinks in: on the roof, at a corner when a crane or antenna is there.
func (p *painter) beaconAt(b *building) (int, int) {
	x := b.cx()
	if b.crane() || b.weather == scene.Summit {
		x = b.x1 - 1
		if x == b.cx() {
			x = b.x0
		}
	}
	return x, (b.base-2*b.built)/2 - 2
}
