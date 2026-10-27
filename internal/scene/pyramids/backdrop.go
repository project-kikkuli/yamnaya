package pyramids

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// backdrop paints everything that holds still: sky, sun or moon, dunes, sand, plateaus and causeways, every
// pyramid not being killed with the haze or sandstorm standing over it, the Nile and its palms.
func (p *painter) backdrop() {
	p.sky()
	p.sun()
	p.moon()
	p.halos()
	p.dunes()
	p.sand()
	for _, n := range p.l.ns {
		p.plateau(n)
	}
	for _, n := range p.l.ns {
		if n.chief {
			p.causeway(n)
		}
	}
	for _, i := range p.l.order {
		if b := &p.l.ps[i]; b.dying == 0 {
			p.stones(b)
		}
	}
	for i := range p.l.ps {
		b := &p.l.ps[i]
		switch {
		case b.dying > 0:
		case b.weather == scene.Clouds:
			p.haze(b)
		case b.weather == scene.Storm:
			p.sandstorm(b)
		case b.weather == scene.Eruption:
			p.dustCloud(b)
		}
	}
	p.river()
	p.palms()
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
				c = p.tint(c, t.weather, t.a*smooth(.35, .9, v), v)
			}
			p.px[y*p.w+x] = c
		}
	}
}

type skyTint struct {
	weather scene.Weather
	a       float64
}

// skyWeather is, per column, the worst weather of the pyramids standing near it, fading at the edges.
func (p *painter) skyWeather() []skyTint {
	out := make([]skyTint, p.w)
	rank := map[scene.Weather]int{scene.Clear: 0, scene.Clouds: 1, scene.Summit: 2, scene.Storm: 3, scene.Eruption: 4}
	for _, b := range p.l.ps {
		if b.weather == scene.Clear {
			continue
		}
		cx, rx := b.cx(), float64(b.x1-b.x0)/2+6
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
		return mix(c, p.pal.stormDark, a*(.75-.3*v))
	case scene.Eruption:
		return mix(c, p.pal.dustShade, a*.5*(.4+.6*v))
	case scene.Summit:
		return mix(c, p.pal.glow, a*.25*smooth(.3, 1, v))
	case scene.Clouds:
		return mix(c, p.pal.haze, a*.3*smooth(.2, 1, v))
	}
	return c
}

// sun crosses the sky from east to west through the day, low and warm near the horizon.
func (p *painter) sun() {
	if p.pal.day < .15 || p.hour < 5.5 || p.hour > 20 {
		return
	}
	t := clamp((p.hour-6)/13, 0, 1)
	cx := float64(p.w) * (.93 - .86*t)
	cy := float64(p.hz) * (.88 - .7*math.Sin(math.Pi*t))
	r := 2.6
	for y := max(0, int(cy-14)); y < min(p.hz, int(cy+14)); y++ {
		for x := max(0, int(cx-16)); x < min(p.w, int(cx+16)); x++ {
			d := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy)
			if d < r {
				p.set(x, y, p.pal.sun)
			} else {
				p.blend(x, y, p.pal.sun, .45*p.pal.day*math.Exp(-(d-r)/3.5))
			}
		}
	}
}

func (p *painter) moon() {
	if p.pal.stars < .3 || p.w < 60 {
		return
	}
	cx, cy, r := float64(p.w)*.16, float64(p.hz)*.16, 3.2
	for y := max(0, int(cy-12)); y < min(p.hz, int(cy+12)); y++ {
		for x := max(0, int(cx-12)); x < min(p.w, int(cx+12)); x++ {
			d := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy)
			shadow := math.Hypot(float64(x)+.5-cx+1.4, float64(y)+.5-cy+.7)
			switch {
			case d < r && shadow > r*.92:
				p.set(x, y, hex("#f4edd4"))
			case d >= r:
				p.blend(x, y, hex("#7d86b4"), .3*math.Exp(-(d-r)/2.5))
			}
		}
	}
}

// halos light the sky gold behind a capped pyramid.
func (p *painter) halos() {
	for _, b := range p.l.ps {
		if b.weather != scene.Summit || b.dying > 0 {
			continue
		}
		cx, cy := b.cx(), float64(b.base-b.planned)
		r := float64(b.planned)*.7 + float64(b.x1-b.x0)*.3
		for y := max(0, int(cy-r*1.3)); y < min(p.hz, int(cy+r)); y++ {
			for x := max(0, int(cx-r*1.6)); x < min(p.w, int(cx+r*1.6)); x++ {
				d := math.Hypot((float64(x)+.5-cx)*.7, float64(y)+.5-cy) / r
				p.blend(x, y, p.pal.glow, .5*math.Exp(-d*d*2.2))
			}
		}
	}
}

// dunes roll low along the horizon behind the necropolises.
func (p *painter) dunes() {
	far := mix(p.pal.dune, p.pal.skyAt(.95), .35)
	for x := range p.w {
		h := float64(p.hz) * (.03 + .09*scene.Fbm(0xd0e, float64(x)*.035, 3))
		for y := max(0, p.hz-int(h)); y < p.hz; y++ {
			v := (float64(p.hz-y) / math.Max(1, h))
			p.set(x, y, mix(p.pal.dune, far, .6*v))
		}
	}
}

// sand is the desert floor from the horizon to the river, rippled by the wind.
func (p *painter) sand() {
	for y := p.hz; y < p.nile; y++ {
		near := float64(y-p.hz) / float64(max(1, p.nile-p.hz))
		for x := range p.w {
			n := fbm2(0x5a7d, float64(x)*.09, float64(y)*.45, 2)
			c := mix(p.pal.sand, p.pal.sandShade, .55*smooth(.35, .75, n)+.15*(1-near))
			p.set(x, y, c)
		}
	}
}

// plateau raises a nested necropolis two pixels per level of nesting, its pyramids standing on it.
func (p *painter) plateau(n necropolis) {
	if n.depth == 0 {
		return
	}
	top := p.hz - 2*n.depth
	for x := max(0, n.x0); x < min(p.w, n.x1); x++ {
		for y := top; y < p.hz; y++ {
			c := p.pal.plateau
			if y == top {
				c = mix(c, p.pal.causeway, .35)
			}
			if unit(0x91a7, y*p.w+x) < .15 {
				c = c.Scale(.9)
			}
			p.fill(x, y, c)
		}
	}
}

// causeway paves a chief's royal complex along its foot, linking its pyramids and its nested necropolises.
func (p *painter) causeway(n necropolis) {
	y := p.hz
	for x := max(0, n.x0); x < min(p.w, n.x1); x++ {
		p.fill(x, y, p.pal.causeway)
		p.fill(x, y+1, mix(p.pal.causeway, p.pal.sandShade, .55))
	}
	for _, x := range [2]int{n.x0, n.x1 - 2} {
		for dx := range 2 {
			p.fill(x+dx, y-1, mix(p.pal.causeway, p.pal.stone, .5))
			p.fill(x+dx, y-2, mix(p.pal.causeway, p.pal.shade, .3))
		}
	}
}

// stones lays a pyramid course by course: the face the sun is on lit, the other shaded, a course cased in
// white limestone where a pull request was merged, the gold pyramidion once done. Courses still to come are a
// dotted outline, and a collapsed pyramid has lost its top to the rubble round its foot. Its ramps of mud
// brick hug the faces while it rises.
func (p *painter) stones(b *pyramid) {
	litWest, litEast := mix(p.pal.shade, p.pal.stone, 1-p.east), mix(p.pal.shade, p.pal.stone, p.east)
	cut := b.built
	if b.weather == scene.Eruption {
		cut = b.collapse()
	}
	for k := range b.planned {
		y := b.base - 1 - k
		if y < 0 {
			break
		}
		lo, hi := b.span(k)
		if k >= b.built {
			if k%2 == 0 && b.weather != scene.Eruption {
				p.fill(lo, y, p.pal.sketch)
				p.fill(hi, y, p.pal.sketch)
			}
			continue
		}
		for x := lo; x <= hi; x++ {
			if k >= cut && noise(b.seed+4, float64(x)*.7) < float64(k-cut+1)/float64(max(1, b.built-cut))*.9+.15 {
				continue
			}
			c := litEast
			switch f := float64(x) + .5 - b.cx(); {
			case math.Abs(f) < .5:
				c = mix(litWest, litEast, .5)
			case f < 0:
				c = litWest
			}
			if k%2 == 1 {
				c = c.Scale(.93)
			}
			if unit(b.seed, k*131+x) < .1 {
				c = c.Scale(.88)
			}
			if b.cased[k] {
				c = mix(p.pal.casing, c, .25)
			}
			if b.weather == scene.Summit && k >= b.planned-3 {
				c = mix(p.pal.gold, white, .25*float64(k-b.planned+3))
			}
			p.fill(x, y, c)
		}
	}
	if b.weather == scene.Eruption {
		p.rubble(b)
	}
	if !b.ramps() {
		return
	}
	east := b.hauling()
	surface := mix(p.pal.ramp, p.pal.sand, .35)
	for k := range b.built {
		y := b.base - 1 - k
		lo, hi := b.span(k)
		out := b.out(k)
		for d := 1; d <= out; d++ {
			c := mix(p.pal.ramp, p.pal.sandShade, .15*float64(k%2))
			if d == out {
				c = surface
			}
			p.fill(lo-d, y, c)
			if east {
				p.fill(hi+d, y, c)
			}
		}
	}
}

// rubble heaps the stones a collapse threw down round a pyramid's foot.
func (p *painter) rubble(b *pyramid) {
	w := float64(b.x1-b.x0)*.35 + 2
	for _, side := range [2]float64{-1, 1} {
		edge := float64(b.x0) - .5
		if side > 0 {
			edge = float64(b.x1) + .5
		}
		for x := int(edge - w); x <= int(edge+w); x++ {
			fx := float64(x) + .5
			h := (1 - math.Abs(fx-edge)/w) * (2 + 2*unit(b.seed+8, x))
			for y := b.base - int(h); y < b.base; y++ {
				c := mix(p.pal.shade, p.pal.stone, unit(b.seed+9, y*97+x)*.7)
				p.fill(x, y, c)
			}
		}
	}
}

// haze hangs a pale shimmer of heat round a quiet pyramid's upper courses.
func (p *painter) haze(b *pyramid) {
	cx := b.cx()
	top := float64(b.base - b.planned)
	rx := float64(b.x1-b.x0)/2 + 4
	y0, y1 := top-5, float64(b.base)
	for y := max(0, int(y0)); y < min(p.hz, int(y1)); y++ {
		fy := float64(y) + .5
		band := smooth(y0, y0+4, fy) * smooth(y1, y1-4, fy)
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(b.seed+31, fx*.08, fy*.5, 3)
			a := band * smooth(rx, rx*.5, math.Abs(fx-cx)) * smooth(.3, .7, n) * .5
			p.blend(x, y, p.pal.haze, a)
		}
	}
}

// sandstorm drives a wall of blown sand across a stuck pyramid, darker at its heart.
func (p *painter) sandstorm(b *pyramid) {
	cx, top, rx := b.cx(), float64(b.base-b.planned-6), float64(b.x1-b.x0)/2+6
	for y := max(0, int(top)); y < min(p.nile, b.base+3); y++ {
		fy := float64(y) + .5
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(b.seed+77, fx*.07, fy*.4, 3)
			a := smooth(rx, rx*.6, math.Abs(fx-cx)) * smooth(top, top+5, fy) * smooth(.28, .62, n) * .82
			p.blend(x, y, mix(p.pal.stormDark, p.pal.storm, n), a)
		}
	}
}

// dustCloud rises over a collapsed pyramid.
func (p *painter) dustCloud(b *pyramid) {
	cx, cy := b.cx(), float64(b.base-b.collapse())
	rx, ry := float64(b.x1-b.x0)/2+3, float64(b.planned)*.5+3
	for y := max(0, int(cy-ry)); y < min(p.hz, int(cy+ry*.6)); y++ {
		fy := float64(y) + .5
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
			fx := float64(x) + .5
			n := fbm2(b.seed+55, fx*.12, fy*.25, 3)
			d := math.Hypot((fx-cx)/rx, (fy-cy)/ry)
			p.blend(x, y, mix(p.pal.dustShade, p.pal.dust, n), smooth(1.05, .4, d+(n-.5)*.6)*.7)
		}
	}
}

// river is the Nile across the foot of the screen, reeds on its edge, mirroring the sky, with the near bank.
func (p *painter) river() {
	for x := range p.w {
		p.set(x, p.nile, p.pal.reed)
		if unit(0x2eed, x) < .4 {
			p.set(x, p.nile-1, mix(p.pal.reed, p.pal.sand, .4))
		}
	}
	for y := p.nile + 1; y < p.bank; y++ {
		v := float64(y-p.nile) / float64(max(1, p.bank-p.nile))
		c := mix(p.pal.water, p.pal.skyAt(1-v*.8), .35)
		for x := range p.w {
			p.set(x, y, mix(c, p.pal.water, .25*noise(0x71e, float64(x)*.2+float64(y)*1.3)))
		}
	}
	for y := p.bank; y < p.p; y++ {
		for x := range p.w {
			p.set(x, y, mix(p.pal.sandShade, p.pal.reed, .25+.2*unit(0xba7, y*p.w+x)))
		}
	}
}

// palms stand along the near bank of the Nile, trunks leaning, fronds drooping.
func (p *painter) palms() {
	for x, k := 3, 0; x < p.w-2; k++ {
		h := 4 + int(spread(0xa1a, k)%4)
		lean := int(spread(0xa1b, k)%3) - 1
		root := p.p - 1
		top := root - h
		for y := root; y > top; y-- {
			dx := lean * (root - y) / max(1, h)
			p.set(x+dx, y, p.pal.trunk)
		}
		tx := x + lean
		for _, f := range [...][2]int{{-2, 0}, {-1, 0}, {0, 0}, {1, 0}, {2, 0}, {-3, 1}, {3, 1}, {0, -1}, {-1, -1}, {1, -1}} {
			p.set(tx+f[0], top+f[1], mix(p.pal.palm, p.pal.reed, .25*unit(0xa1c, k*7+f[0])))
		}
		x += 7 + int(spread(0xa1d, k)%9)
	}
}

func (p *painter) starField() {
	if p.pal.stars <= 0 {
		return
	}
	for y := range int(float64(p.hz) * .6) {
		for x := range p.w {
			i := y*p.w + x
			if !p.solid[i] && spread(0x57a5, i)%1000 < 5 {
				p.stars = append(p.stars, star{i, .35 + .65*unit(0x57a6, i)})
			}
		}
	}
}
