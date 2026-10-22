package scene

import "math"

// shore is the dark treeline where the valley meets the lake.
func (p *painter) shore() {
	for x := range p.w {
		fx := float64(x)
		crown := math.Abs(math.Mod(fx+unit(0x5400, int(fx/5))*5, 5)-2.5) / 2.5
		h := (1 + 3.5*(1-crown)) * (.4 + 1.1*fbm(0x5401, fx*.06, 3))
		for y := int(float64(p.hz) - h); y < p.hz; y++ {
			if y >= 0 {
				p.set(x, y, mix(p.th.tree, p.air[y*p.w+x], .15))
				p.solid[y*p.w+x] = true
			}
		}
	}
	p.village()
}

// village is a scatter of warm windows along the shore, the way a valley town looks under the peaks at dusk.
func (p *painter) village() {
	if p.th.stars < .3 {
		return
	}
	for x := range p.w {
		if fbm(0x7111, float64(x)*.04, 2) < .58 || unit(0x7112, x) > .35 {
			continue
		}
		y := p.hz - 1 - int(unit(0x7113, x)*2.5)
		p.set(x, y, mix(hex("#ffc66e"), hex("#ff9a4a"), unit(0x7114, x)))
	}
}

// lake mirrors everything above the horizon, broken by wind-driven ripples.
func (p *painter) lake() {
	depth := float64(p.p - p.hz)
	for y := p.hz; y < p.p; y++ {
		k := float64(y - p.hz)
		src := p.hz - 1 - int(k*1.15)
		fade := .3 + .4*k/math.Max(1, depth)
		for x := range p.w {
			fx := float64(x)
			shift := math.Sin(k*.6+fx*.03) * math.Min(2, .12*k+.3)
			sx := min(p.w-1, max(0, x+int(math.Round(shift))))
			var c rgb
			if src >= 0 {
				c = p.px[src*p.w+sx].scale(.72)
			} else {
				c = p.th.skyAt(0)
			}
			c = mix(c, p.th.water, fade)
			if y-p.hz < 1 {
				c = mix(c, p.th.tree, .3)
			}
			p.px[y*p.w+x] = c
		}
	}
}
