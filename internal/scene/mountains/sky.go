package mountains

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func (p *painter) sky() {
	tint := p.skyWeather()
	for y := range p.hz {
		v := float64(y) / float64(p.hz)
		base := p.th.skyAt(v)
		for x := range p.w {
			c := base
			if w := tint[x]; w.a > 0 {
				c = p.tint(c, w.weather, w.a, v)
			}
			p.px[y*p.w+x] = c
		}
	}
	p.moon()
}

type skyTint struct {
	weather scene.Weather
	a       float64
}

// skyWeather is, per column, the worst weather of the ranges standing there, fading in at their edges.
func (p *painter) skyWeather() []skyTint {
	out := make([]skyTint, p.w)
	rank := map[scene.Weather]int{scene.Clear: 0, scene.Clouds: 1, scene.Summit: 2, scene.Storm: 3, scene.Eruption: 4}
	for _, m := range p.l.massifs {
		if m.weather == scene.Clear {
			continue
		}
		for x := max(0, int(m.x0)); x < min(p.w, int(m.x1)+1); x++ {
			fx := float64(x) + .5
			a := smooth(m.x0-2, m.x0+5, fx) * smooth(m.x1+2, m.x1-5, fx)
			cur := out[x]
			if rank[m.weather] > rank[cur.weather] || m.weather == cur.weather && a > cur.a {
				out[x] = skyTint{m.weather, a}
			}
		}
	}
	return out
}

func (p *painter) tint(c rgb, w scene.Weather, a, v float64) rgb {
	switch w {
	case scene.Storm:
		return mix(c, p.th.stormSky, a*(.8-.35*v))
	case scene.Eruption:
		c = mix(c, p.th.smokeDark, a*.45*(1-v))
		return mix(c, p.th.lavaDeep, a*.55*smooth(.35, 1, v))
	case scene.Summit:
		return mix(c, p.th.glow, a*.25*smooth(.3, 1, v))
	case scene.Clouds:
		return mix(c, rgb{R: c.Luma(), G: c.Luma(), B: c.Luma()}, a*.35)
	}
	return c
}

func (p *painter) moon() {
	if p.th.stars < .3 || p.w < 60 {
		return
	}
	cx, cy, r := float64(p.w)*.86, float64(p.hz)*.15, 3.6
	for y := int(cy - 12); y < int(cy+12); y++ {
		for x := int(cx - 12); x < int(cx+12); x++ {
			if x < 0 || x >= p.w || y < 0 || y >= p.hz {
				continue
			}
			d := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy)
			shadow := math.Hypot(float64(x)+.5-cx-1.4, float64(y)+.5-cy+.7)
			switch {
			case d < r && shadow > r*.92:
				p.set(x, y, hex("#f4edd4"))
			case d >= r:
				p.blend(x, y, hex("#7d86b4"), .3*math.Exp(-(d-r)/2.5))
			}
		}
	}
}

// halos light the sky behind summits reached and craters erupting, before the mountains go in front.
func (p *painter) halos() {
	for i, m := range p.l.mounts {
		g := p.geo[i]
		var c rgb
		var a, r float64
		switch m.weather {
		case scene.Summit:
			c, a, r = p.th.glow, .6, math.Max(5, g.height*.5)
		case scene.Eruption:
			c, a, r = p.th.lava, .75, math.Max(6, g.height*.6)
		default:
			continue
		}
		for y := max(0, int(g.y-r*1.5)); y < min(p.hz, int(g.y+r)); y++ {
			for x := max(0, int(g.x-r*1.5)); x < min(p.w, int(g.x+r*1.5)); x++ {
				d := math.Hypot(float64(x)+.5-g.x, (float64(y)+.5-g.y)*1.2) / r
				p.blend(x, y, c, a*math.Exp(-d*d*2.2))
			}
		}
	}
}

// wisps are thin high clouds drifting across the sky, lit from below near the horizon.
func (p *painter) wisps() {
	for y := int(float64(p.hz) * .14); y < int(float64(p.hz)*.6); y++ {
		v := float64(y) / float64(p.hz)
		band := smooth(.14, .26, v) * smooth(.6, .45, v)
		col := mix(p.th.cloudShade, p.th.cloud, smooth(.25, .6, v))
		for x := range p.w {
			n := fbm2(0xc10d, float64(x)*.03, float64(y)*.28, 4)
			p.blend(x, y, col, smooth(.56, .76, n)*.5*band)
		}
	}
}
