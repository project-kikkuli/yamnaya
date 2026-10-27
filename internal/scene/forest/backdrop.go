package forest

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// backdrop paints everything that holds still: sky, sun or moon, hills, the forest floor, groves and clearings,
// the glow behind trees in bloom or on fire, every tree standing still, and fog and storm cloud over them.
func (p *painter) backdrop() {
	p.sky()
	p.lights()
	p.hills()
	p.floor()
	for _, g := range p.l.gs {
		p.grove(g)
	}
	for i := range p.l.ts {
		p.halo(&p.l.ts[i])
	}
	for i := range p.l.ts {
		if t := &p.l.ts[i]; !t.moving() {
			p.tree(t, upright)
		}
	}
	for i := range p.l.ts {
		t := &p.l.ts[i]
		switch {
		case t.dying > 0:
		case t.weather == scene.Clouds:
			p.fog(t)
		case t.weather == scene.Storm:
			p.cloud(t)
		case t.weather == scene.Eruption:
			p.smoke(t)
		}
	}
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

// skyWeather is, per column, the worst weather of the trees standing near it, fading at the edges.
func (p *painter) skyWeather() []skyTint {
	out := make([]skyTint, p.w)
	rank := map[scene.Weather]int{scene.Clear: 0, scene.Clouds: 1, scene.Summit: 2, scene.Storm: 3, scene.Eruption: 4}
	for _, t := range p.l.ts {
		if t.weather == scene.Clear || t.dying > 0 {
			continue
		}
		cx, rx := float64(t.cx)+.5, t.rx+6
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			a := smooth(rx, rx-4, math.Abs(float64(x)+.5-cx))
			cur := out[x]
			if rank[t.weather] > rank[cur.weather] || t.weather == cur.weather && a > cur.a {
				out[x] = skyTint{t.weather, a}
			}
		}
	}
	return out
}

func (p *painter) tint(c rgb, w scene.Weather, a, v float64) rgb {
	switch w {
	case scene.Storm:
		return mix(c, p.pal.stormSky, a*.6*smooth(.25, .8, v))
	case scene.Eruption:
		return mix(c, p.pal.fire.Scale(.55), a*.45*smooth(.45, 1, v))
	case scene.Summit:
		return mix(c, p.pal.glow, a*.2*smooth(.3, 1, v))
	case scene.Clouds:
		return mix(c, p.pal.fogShade, a*.3*smooth(.1, 1, v))
	}
	return c
}

// lights hangs the sun by day and the moon by night where the clock puts them on their arc over the forest.
func (p *painter) lights() {
	if p.w < 60 {
		return
	}
	day := (p.hour - 6) / 12
	body, glow, r := p.pal.sun, p.pal.glow, 2.6
	if day < 0 || day > 1 {
		day = math.Mod(p.hour+6, 24) / 12
		body, glow, r = p.pal.moon, hex("#7d86b4"), 2.2
		if p.pal.stars < .2 {
			return
		}
	}
	cx := float64(p.w) * (.08 + .84*day)
	cy := float64(p.hz) * (.62 - .5*math.Sin(math.Pi*day))
	for y := max(0, int(cy-14)); y < min(p.hz, int(cy+14)); y++ {
		for x := max(0, int(cx-16)); x < min(p.w, int(cx+16)); x++ {
			d := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy)
			if d < r {
				p.fill(x, y, body)
				continue
			}
			p.blend(x, y, glow, .35*math.Exp(-(d-r)/3))
		}
	}
}

// hills are the forest beyond the groves: two hazy ridges of firs, the nearer darker.
func (p *painter) hills() {
	for k, layer := range [2]struct {
		c           rgb
		base, swell float64
		tooth       int
		seed        uint64
	}{{mix(p.pal.hillFar, p.pal.skyAt(.9), .35), .3, .16, 4, 0xf01}, {p.pal.hillNear, .16, .1, 6, 0xf02}} {
		for x := range p.w {
			top := float64(p.hz) * (1 - layer.base - layer.swell*fbm(layer.seed, float64(x)*.018, 3))
			at := x % layer.tooth
			peak := float64(layer.tooth)/2 - math.Abs(float64(at)-float64(layer.tooth-1)/2)
			top -= peak * (.6 + 1.2*unit(layer.seed, x/layer.tooth)) * float64(k+1) * .7
			for y := max(0, int(top)); y < p.hz; y++ {
				c := layer.c
				if k == 1 {
					c = mix(c, p.pal.skyAt(float64(y)/float64(p.hz)), .12)
				}
				p.fill(x, y, c)
			}
		}
	}
}

// floor is the grass line, the forest floor of soil and fallen leaves under it darkening into shade, and ferns
// in the foreground.
func (p *painter) floor() {
	deep := float64(max(1, p.p-p.hz-3))
	for x := range p.w {
		p.fill(x, p.hz, p.pal.grassLit)
		for y := p.hz + 1; y < min(p.p, p.hz+3); y++ {
			p.fill(x, y, p.pal.grass)
		}
		for y := p.hz + 3; y < p.p; y++ {
			n := fbm2(0xf1, float64(x)*.12, float64(y)*.3, 2)
			c := mix(p.pal.soil, p.pal.litter, .6*smooth(.5, .8, n))
			c = mix(c, p.pal.moss, .6*smooth(.62, .3, n))
			p.fill(x, y, c.Scale(1-.45*smooth(0, 1, float64(y-p.hz-3)/deep)))
		}
		if spread(0xf3, x)%4 == 0 {
			p.fill(x, p.hz-1, p.pal.grass)
		}
		if k := spread(0xf7, x/9) % 9; k == 0 && x%9 < 2 {
			p.fill(x, p.hz-2, mix(p.pal.flower, p.pal.litter, .45))
			if x%9 == 0 {
				p.fill(x, p.hz-1, p.pal.birch)
			}
		}
		p.brook(x)
		fern := int(12 * smooth(.42, .8, fbm(0xf4, float64(x)*.06, 3)))
		for y := p.p - fern; y < p.p; y++ {
			if fern-(p.p-y) < 2 && spread(0xf6, y*p.w+x)%2 == 0 {
				continue
			}
			lit := unit(0xf5, y*p.w+x)
			p.fill(x, y, mix(p.pal.leafShade.Scale(.7), p.pal.needle, .5*lit))
		}
	}
}

// brookAt is the top pixel row of the brook winding across the forest floor in column x.
func (p *painter) brookAt(x int) int {
	deep := float64(p.p - p.hz - 3)
	return p.hz + 3 + int(deep*.4+3*math.Sin(float64(x)*.045)+4*(fbm(0xb7, float64(x)*.03, 2)-.5))
}

// brook paints column x of the brook: a bank, three rows of water lit by the sky, and stones along it.
func (p *painter) brook(x int) {
	y := p.brookAt(x)
	water := mix(p.pal.skyAt(.85), p.pal.soil, .45)
	p.fill(x, y-1, mix(p.pal.grassLit, p.pal.soil, .5))
	for k := range 3 {
		p.fill(x, y+k, mix(water, p.pal.skyAt(.4), .25*float64(k)).Scale(1-.12*float64(k)))
	}
	if spread(0xb8, x)%11 == 0 {
		p.fill(x, y+3, mix(p.pal.fogShade, p.pal.soil, .3))
		p.fill(x+1, y+3, mix(p.pal.fogShade, p.pal.soil, .5))
	}
}

// grove marks a group's ground: a repository's grove is dark moss with ferns between its trees; a chief's
// clearing is light meadow with flowers, a hedge round it at each side, and a nested one rises on a knoll.
func (p *painter) grove(g grove) {
	top := p.hz - 2*g.depth
	span := float64(g.x1 - g.x0)
	for x := max(0, g.x0); x < min(p.w, g.x1); x++ {
		if !g.chief {
			for y := top; y < top+2; y++ {
				p.fill(x, y, p.pal.moss)
			}
			fern := int(3.5 * smooth(.35, .75, fbm(g.seed, float64(x)*.35, 2)))
			for y := top - fern; y < top; y++ {
				p.fill(x, y, mix(p.pal.leafShade, p.pal.moss, unit(g.seed, y*p.w+x)))
			}
			continue
		}
		edge := math.Min(float64(x-g.x0), float64(g.x1-1-x))
		lift := top + int(math.Round(2*float64(g.depth)*smooth(3, 0, edge)))
		for y := lift; y < p.hz+3; y++ {
			c := p.pal.meadow
			if y == lift {
				c = mix(c, p.pal.grassLit, .5)
			}
			p.fill(x, y, c)
		}
		if spread(g.seed, x)%5 == 0 {
			p.fill(x, lift-1, p.pal.flower)
		}
	}
	if !g.chief || span < 12 {
		return
	}
	for _, at := range [2]float64{float64(g.x0) + 2.5, float64(g.x1) - 2.5} {
		for y := top - 4; y < top; y++ {
			for x := int(at - 3); x <= int(at+3); x++ {
				d := math.Hypot((float64(x)+.5-at)/2.6, (float64(y)+.5-float64(top))/3.4)
				n := fbm2(g.seed+1, float64(x)*.6, float64(y)*.6, 2)
				if d+(n-.5)*.4 < 1 {
					p.fill(x, y, mix(p.pal.hedge, p.pal.leafLit, smooth(.5, .9, n)*.5))
				}
			}
		}
	}
}

// halo is the warm light behind a tree in full bloom and the orange glow of one on fire.
func (p *painter) halo(t *tree) {
	var c rgb
	var a float64
	switch {
	case t.dying > 0:
		return
	case t.weather == scene.Summit:
		c, a = p.pal.glow, .5
	case t.weather == scene.Eruption:
		c, a = p.pal.fire, .55
	default:
		return
	}
	cx, cy := float64(t.cx)+.5, float64(t.base)-t.vc
	r := t.ry + t.rx + 2
	for y := max(0, int(cy-r*1.4)); y < min(p.hz, int(cy+r)); y++ {
		for x := max(0, int(cx-r*1.6)); x < min(p.w, int(cx+r*1.6)); x++ {
			d := math.Hypot((float64(x)+.5-cx)*.8, float64(y)+.5-cy) / r
			p.blend(x, y, c, a*math.Exp(-d*d*2.2))
		}
	}
}

// fog lies in drifts round a quiet tree's trunk and lower crown.
func (p *painter) fog(t *tree) {
	cx := float64(t.cx) + .5
	rx := t.rx + 6
	y0, y1 := float64(t.base)-float64(t.h)*.8, float64(t.base)+2
	for y := max(0, int(y0)); y < min(p.p, int(y1)); y++ {
		fy := float64(y) + .5
		band := smooth(y0, y0+5, fy) * smooth(y1, y1-3, fy)
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(t.seed+31, fx*.1, fy*.25, 3)
			a := band * smooth(rx, rx*.5, math.Abs(fx-cx)) * smooth(.34, .62, n) * .75
			p.blend(x, y, mix(p.pal.fogShade, p.pal.fog, n), a)
		}
	}
}

// cloud is a storm's dark cloud over a stuck tree, lit along its top.
func (p *painter) cloud(t *tree) {
	cx, cy, rx, ry := t.stormShape()
	for y := max(0, int(cy-ry*1.6)); y < min(p.hz, int(cy+ry*1.3)); y++ {
		fy := float64(y) + .5
		for x := max(0, int(cx-rx*1.2)); x < min(p.w, int(cx+rx*1.2)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(t.seed+77, fx*.15, fy*.3, 3)
			d := math.Hypot((fx-cx)/rx, (fy-cy)/ry)
			a := smooth(1.15, .6, d+(n-.5)*.8)
			p.blend(x, y, mix(p.pal.stormCloud, p.pal.stormTop, smooth(cy+ry*.2, cy-ry, fy)+(n-.5)*.5), a*.97)
		}
	}
}

func (t *tree) stormShape() (cx, cy, rx, ry float64) {
	ry = 3 + t.rx*.25
	return float64(t.cx) + .5, math.Max(ry+1, float64(t.base-t.h)-5), t.rx + 6, ry
}

// smoke rises in a widening column from a burning tree.
func (p *painter) smoke(t *tree) {
	cx, top := float64(t.cx)+.5, float64(t.base-t.h)
	reach := math.Max(8, float64(t.h)*.9)
	for y := max(0, int(top-reach)); y < int(top)+2; y++ {
		fy := float64(y) + .5
		up := clamp((top-fy)/reach, 0, 1)
		rx := t.rx*.5 + 1 + up*5
		for x := max(0, int(cx-rx-3)); x < min(p.w, int(cx+rx+6)); x++ {
			fx := float64(x) + .5
			n := fbm2(t.seed+41, fx*.18, fy*.15, 3)
			a := smooth(rx, rx*.3, math.Abs(fx-cx-up*3)+(n-.5)*3) * (1 - up) * .6
			p.blend(x, y, mix(p.pal.smoke, p.pal.char, n), a)
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
