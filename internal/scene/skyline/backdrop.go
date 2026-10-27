package skyline

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// backdrop paints everything that holds still: sky, far towers, street, water, and every building that is not
// being demolished, with the smog and storm cloud standing over it.
func (p *painter) backdrop() {
	p.sky()
	p.moon()
	p.halos()
	p.far()
	p.street()
	for _, d := range p.l.ds {
		p.podium(d)
	}
	for i := range p.l.bs {
		if b := &p.l.bs[i]; b.dying == 0 {
			p.facade(b, 0)
		}
	}
	for i := range p.l.bs {
		b := &p.l.bs[i]
		switch {
		case b.dying > 0:
		case b.weather == scene.Clouds:
			p.smog(b)
		case b.weather == scene.Storm:
			p.cloud(b)
		}
	}
	p.reflect()
	p.starField()
}

func (p *painter) sky() {
	tint := p.skyWeather()
	for y := range p.hz {
		v := float64(y) / float64(p.hz)
		base := p.pal.skyAt(v)
		for x := range p.w {
			c := base
			if t := tint[x]; t.a > 0 {
				c = p.tint(c, t.weather, t.a, v)
			}
			p.px[y*p.w+x] = c
		}
	}
}

type skyTint struct {
	weather scene.Weather
	a       float64
}

// skyWeather is, per column, the worst weather of the buildings standing near it, fading at the edges.
func (p *painter) skyWeather() []skyTint {
	out := make([]skyTint, p.w)
	rank := map[scene.Weather]int{scene.Clear: 0, scene.Clouds: 1, scene.Summit: 2, scene.Storm: 3, scene.Eruption: 4}
	for _, b := range p.l.bs {
		if b.weather == scene.Clear {
			continue
		}
		cx, rx := float64(b.x0+b.x1)/2, float64(b.x1-b.x0)/2+6
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			a := smooth(rx, rx-4, math.Abs(float64(x)+.5-cx))
			cur := out[x]
			if rank[b.weather] > rank[cur.weather] || b.weather == cur.weather && a > cur.a {
				out[x] = skyTint{b.weather, a}
			}
		}
	}
	return out
}

func (p *painter) tint(c rgb, w scene.Weather, a, v float64) rgb {
	switch w {
	case scene.Storm:
		return mix(c, p.pal.stormSky, a*(.8-.35*v))
	case scene.Eruption:
		c = mix(c, p.pal.smoke, a*.45*(1-v))
		return mix(c, p.pal.fire.Scale(.55), a*.5*smooth(.3, 1, v))
	case scene.Summit:
		return mix(c, p.pal.glow, a*.22*smooth(.3, 1, v))
	case scene.Clouds:
		return mix(c, p.pal.smog, a*.35*smooth(.1, 1, v))
	}
	return c
}

func (p *painter) moon() {
	if p.pal.stars < .3 || p.w < 60 {
		return
	}
	cx, cy, r := float64(p.w)*.88, float64(p.hz)*.14, 3.4
	for y := max(0, int(cy-12)); y < min(p.hz, int(cy+12)); y++ {
		for x := max(0, int(cx-12)); x < min(p.w, int(cx+12)); x++ {
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

// halos light the sky behind a finished tower and the fires of a kaiju attack.
func (p *painter) halos() {
	for _, b := range p.l.bs {
		var c rgb
		var a float64
		switch b.weather {
		case scene.Summit:
			c, a = p.pal.glow, .55
		case scene.Eruption:
			c, a = p.pal.fire, .6
		default:
			continue
		}
		cx, cy := float64(b.x0+b.x1)/2, float64(b.base-2*b.built)
		r := math.Max(6, float64(b.base)-cy)*.55 + float64(b.x1-b.x0)*.5
		for y := max(0, int(cy-r*1.4)); y < min(p.hz, int(cy+r)); y++ {
			for x := max(0, int(cx-r*1.6)); x < min(p.w, int(cx+r*1.6)); x++ {
				d := math.Hypot((float64(x)+.5-cx)*.8, float64(y)+.5-cy) / r
				p.blend(x, y, c, a*math.Exp(-d*d*2.2))
			}
		}
	}
}

// far is the city beyond the districts: a hazy run of towers with a few windows lit after dark.
func (p *painter) far() {
	body := mix(p.pal.far, p.pal.skyAt(.92), .3)
	lit := mix(body, p.pal.farLit, .6)
	x := 0
	for k := 0; x < p.w; k++ {
		bw := 2 + int(spread(0xfa7, k)%6)
		h := int(float64(p.hz) * (.08 + .26*unit(0xfa8, k)))
		for xx := x; xx < min(p.w, x+bw); xx++ {
			for y := max(0, p.hz-h); y < p.hz; y++ {
				c := body
				if (y-p.hz+h)%2 == 1 && (xx-x)%2 == 1 && unit(0xfa9, y*p.w+xx) < .4*p.pal.lights {
					c = lit
				}
				p.fill(xx, y, c)
			}
		}
		x += bw + int(spread(0xfaa, k)%2)
	}
}

// street is the sidewalk, the road with its lane line and lamps, the quay and the river below.
func (p *painter) street() {
	curb := mix(p.pal.sidewalk, p.pal.label, .12)
	lane := mix(p.pal.road, p.pal.lane, .55)
	quay := mix(p.pal.sidewalk, p.pal.slab, .5)
	for x := range p.w {
		p.fill(x, p.hz, curb)
		p.fill(x, p.hz+1, p.pal.sidewalk)
		for y := p.hz + 2; y < p.hz+6; y++ {
			p.fill(x, y, p.pal.road)
		}
		if x/3%2 == 0 {
			p.fill(x, p.hz+4, lane)
		}
		p.fill(x, p.hz+6, quay)
		for y := p.hz + 7; y < p.p; y++ {
			p.set(x, y, p.pal.water)
		}
	}
	lamp := mix(p.pal.slab, p.pal.lamp, .4+.6*p.pal.lights)
	for x := 6; x < p.w; x += 12 {
		p.fill(x, p.hz, lamp)
		for dy := 1; dy < 5; dy++ {
			for dx := -2; dx <= 2; dx++ {
				p.blend(x+dx, p.hz+dy, p.pal.lamp, .35*p.pal.lights*math.Exp(-float64(dx*dx+dy*dy)/4))
			}
		}
	}
}

// podium raises a nested district a floor per level of nesting, its buildings standing on it.
func (p *painter) podium(d district) {
	if d.depth == 0 {
		return
	}
	top := p.hz - 2*d.depth
	for x := max(0, d.x0); x < min(p.w, d.x1); x++ {
		for y := top; y < p.hz; y++ {
			c := p.pal.podium
			if y == top {
				c = mix(c, p.pal.label, .15)
			}
			p.fill(x, y, c)
		}
	}
}

// smog hangs a brown haze round a quiet building's upper floors.
func (p *painter) smog(b *building) {
	cx := float64(b.x0+b.x1) / 2
	top := float64(b.base - 2*b.planned)
	rx := float64(b.x1-b.x0)/2 + 5
	y0, y1 := top-6, float64(b.base)-(float64(b.base)-top)*.3
	for y := max(0, int(y0)); y < min(p.hz, int(y1)); y++ {
		fy := float64(y) + .5
		band := smooth(y0, y0+5, fy) * smooth(y1, y1-6, fy)
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(b.seed+31, fx*.11, fy*.2, 3)
			a := band * smooth(rx, rx*.55, math.Abs(fx-cx)) * smooth(.36, .66, n) * .8
			p.blend(x, y, mix(p.pal.smogShade, p.pal.smog, n), a)
		}
	}
}

// cloud is a storm's dark cloud over a stuck building, lit along its top.
func (p *painter) cloud(b *building) {
	cx, cy, rx, ry := b.stormShape()
	for y := max(0, int(cy-ry*1.6)); y < min(p.hz, int(cy+ry*1.3)); y++ {
		fy := float64(y) + .5
		for x := max(0, int(cx-rx*1.2)); x < min(p.w, int(cx+rx*1.2)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(b.seed+77, fx*.15, fy*.3, 3)
			d := math.Hypot((fx-cx)/rx, (fy-cy)/ry)
			a := smooth(1.15, .6, d+(n-.5)*.8)
			p.blend(x, y, mix(p.pal.stormCloud, p.pal.stormTop, smooth(cy+ry*.2, cy-ry, fy)+(n-.5)*.5), a*.97)
		}
	}
}

func (b *building) stormShape() (cx, cy, rx, ry float64) {
	w := float64(b.x1 - b.x0)
	ry = 3 + w*.15
	return float64(b.x0+b.x1) / 2, math.Max(ry+1, float64(b.base-2*b.planned)-6), w/2 + 5, ry
}

// reflect mirrors the city in the river, darker and broken by the current.
func (p *painter) reflect() {
	wt := p.hz + 7
	for y := wt; y < p.p; y++ {
		src := p.hz - 1 - (y - wt)
		if src < 0 {
			break
		}
		deep := float64(y-wt) / float64(max(1, p.p-wt))
		dx := int(math.Round((noise(0x7e1, float64(y)*.9) - .5) * 3))
		for x := range p.w {
			sx := min(p.w-1, max(0, x+dx))
			c := mix(p.pal.water, p.px[src*p.w+sx], .5*(1-.6*deep))
			p.px[y*p.w+x] = c.Scale(.85)
		}
	}
}

func (p *painter) starField() {
	if p.pal.stars <= 0 {
		return
	}
	for y := range int(float64(p.hz) * .55) {
		for x := range p.w {
			i := y*p.w + x
			if !p.solid[i] && spread(0x57a5, i)%1000 < 4 {
				p.stars = append(p.stars, star{i, .35 + .65*unit(0x57a6, i)})
			}
		}
	}
}
