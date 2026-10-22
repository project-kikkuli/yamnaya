package scene

import "math"

func (p *painter) ridge() {
	for x := range p.w {
		p.column(x, p.far, -1, -1, .6)
	}
}

func (p *painter) ground(d int) {
	lay := p.layers[d]
	fog := .28 * (1 - float64(d)/float64(max(1, p.l.depth)))
	if p.l.depth == 0 {
		fog = .12
	}
	for x := range p.w {
		p.column(x, lay.hm, d, lay.own[x], fog)
	}
}

// column paints one column of a ridgeline from its crest down to the valley floor.
func (p *painter) column(x int, hm []float64, d, owner int, fog float64) {
	h := hm[x]
	if h <= 0 {
		return
	}
	surf := float64(p.hz) - h
	for y := max(0, int(surf)); y < p.hz; y++ {
		cover := clamp(float64(y+1)-surf, 0, 1)
		c := p.shade(x, y, hm, d, owner, fog)
		p.px[y*p.w+x] = mix(p.px[y*p.w+x], c, cover)
		if cover > .5 {
			p.solid[y*p.w+x] = true
		}
	}
}

func (p *painter) shade(x, y int, hm []float64, d, owner int, fog float64) rgb {
	th := p.th
	fx, fy := float64(x), float64(y)
	surf := float64(p.hz) - hm[x]
	below := fy - surf
	left, right := hm[max(0, x-1)], hm[min(p.w-1, x+1)]
	slope := (right - left) / 2
	lit := smooth(-1.1, 1.1, slope)
	alt := (float64(p.hz) - fy) / p.layerHeight(0)
	seed := uint64(0x91d3 + d)
	var m *mount
	if owner >= 0 {
		m = &p.l.mounts[owner]
		seed = m.seed
	}
	if m != nil && m.weather == Clouds {
		lit = .5 + (lit-.5)*.45
	}

	// Below the crest the light follows the faces of a pyramid: west lit by the low sun, east in shadow,
	// with gullies running parallel to each face's edge.
	steep := smooth(1.2, 2.6, math.Abs(slope)) * smooth(4, 0, below)
	across := fx + below*math.Copysign(.8, -slope)
	if m != nil {
		g := p.geo[owner]
		drop := math.Max(0, fy-g.y)
		ridge := g.x + (fbm(seed+13, fy*.12, 2)-.5)*g.half*.5*math.Min(1, drop/g.height)
		side := fx + .5 - ridge
		lit = lerp(lit, .1+.85*smooth(1.2, -1.2, side), smooth(0, 3, below))
		grade := g.half / math.Max(1, g.height)
		across = fx - math.Copysign(drop*grade, side)
	} else {
		lit = lerp(lit, .4, smooth(2, 10, below))
	}
	snowline := .42 + .14*(fbm(seed, fx*.06, 3)-.5)
	streak := fbm2(seed+3, across*.45, fy*.05, 3)
	snow := smooth(snowline-.05, snowline+.05, alt+(streak-.5)*.5)
	snow *= 1 - .8*steep
	if below < 1.6 && alt > snowline*.65 {
		snow = math.Max(snow, .9)
	}
	treeline := .15 + .07*(fbm(seed+5, fx*.1, 2)-.5)
	trees := smooth(treeline+.03, treeline-.03, alt)

	var crater float64
	if m != nil && m.weather == Eruption {
		g := p.geo[owner]
		crater = math.Hypot(fx+.5-g.x, fy-g.y) / math.Max(3, g.height)
		snow *= smooth(.18, .5, crater)
	}

	rock := mix(th.rockShade, th.rockLit, lit).scale(.82 + .36*noise2(seed+7, across*.7, fy*.3))
	snowC := mix(th.snowShade, th.snowLit, math.Pow(lit, .8))
	if m != nil && m.weather == Summit {
		snowC = mix(snowC, th.glow, .45*lit)
	}
	c := mix(rock, snowC, snow)
	speck := smooth(.55, .8, noise2(seed+11, fx*1.3, fy*.9))
	c = mix(c, mix(th.forest, th.forestSnow, speck*.6), trees)
	c = c.scale(th.valley + (1-th.valley)*smooth(0, .55, alt))

	if m != nil {
		switch m.weather {
		case Storm:
			c = mix(c, th.stormSky, .45).scale(.8)
		case Eruption:
			c = mix(c.scale(.55), th.lava, .7*math.Exp(-crater*7))
		}
		if m.selected {
			c = c.scale(1.12)
		}
	}
	c = mix(c, p.air[y*p.w+x], fog+.1*(1-smooth(0, .5, alt)))
	if below < 1 && lit > .45 {
		rim := th.rim
		if m != nil && m.weather == Summit {
			rim = th.glow
		}
		c = mix(c, rim, (.35+.3*boolf(m != nil && m.selected))*lit)
	}
	return c
}

func boolf(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
