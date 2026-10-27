package pyramids

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// motion paints what moves over a copy of the backdrop: the desert's own life, heat haze shimmering, stones
// falling from a collapse, kills sliding into sand, the selection's light and the beacons, then the glyphs.
func (p *painter) motion() {
	p.ambient()
	for i := range p.l.ps {
		b := &p.l.ps[i]
		if b.dying > 0 {
			p.slide(b)
			continue
		}
		switch b.weather {
		case scene.Clouds:
			p.shimmer(b)
		case scene.Eruption:
			p.falling(b)
		}
		if b.selected {
			p.shaft(b)
		}
		if b.needsYou {
			p.beaconGlow(b)
		}
	}
	p.claimPyramids()
	p.sprites()
}

// claimPyramids marks every cell a pyramid stands in as its own, so a click there selects it.
func (p *painter) claimPyramids() {
	for _, i := range p.l.order {
		b := &p.l.ps[i]
		if b.dying >= 1 {
			continue
		}
		for y := max(0, b.apexRow()); y < b.groundRow(); y++ {
			for x := max(0, b.westAt(y)); x <= min(p.w-1, b.eastAt(y)); x++ {
				p.who[y*p.w+x] = i + 1
			}
		}
	}
}

// ambient takes turns on one beat: stars twinkle, the Nile glints and a felucca sails along it.
func (p *painter) ambient() {
	beat := p.frame / int(scene.Ambient/scene.Step)
	for _, s := range p.stars {
		b := s.b * (.45 + .55*unit(0x57a7, s.i*31+beat))
		p.px[s.i] = mix(p.px[s.i], white, b*p.pal.stars)
	}
	glint := mix(p.pal.skyAt(.97), p.pal.sun, .4)
	for y := p.nile + 1; y < p.bank; y++ {
		for x := range p.w {
			i := y*p.w + x
			if spread(0x91e, i*7+beat)%100 < 3 {
				p.px[i] = mix(p.px[i], glint, .5)
			}
		}
	}
	p.felucca(beat)
	p.caravan(beat)
}

// caravan is a line of camels plodding east across the sand between the necropolises and the river.
func (p *painter) caravan(beat int) {
	if p.nile-p.hz < 8 {
		return
	}
	span := p.w + 30
	y := p.hz + (p.nile-p.hz)*3/5
	c := mix(p.pal.trunk, p.pal.shade, .3)
	for k := range 3 {
		x := (beat+int(spread(0xca1, p.w)%uint64(span))+k*8)%span - 24
		for _, d := range camel {
			p.set(x+d[0], y+d[1], c)
		}
		step := (beat + k) % 2
		p.set(x+step, y, c)
		p.set(x+3-step, y, c)
	}
}

// camel is a camel facing east, its feet on row 0, legs drawn apart by the caravan's step.
var camel = [...][2]int{{0, -1}, {3, -1}, {0, -2}, {1, -2}, {2, -2}, {3, -2}, {1, -3}, {2, -3}, {4, -2}, {4, -3}, {5, -4}, {6, -4}}

// felucca is a little sailing boat drifting downstream on the Nile.
func (p *painter) felucca(beat int) {
	if p.bank-p.nile < 4 {
		return
	}
	span := p.w + 12
	x := span - 1 - (beat+int(spread(0xfe1, p.w)%uint64(span)))%span - 6
	y := (p.nile + p.bank) / 2
	for dx := range 5 {
		p.set(x+dx, y, p.pal.trunk)
	}
	for h := 1; h <= 4; h++ {
		for dx := 1; dx <= 5-h; dx++ {
			p.set(x+dx, y-h, p.pal.sail)
		}
	}
	p.set(x+1, y-5, p.pal.trunk)
}

// shimmer bends the rows round a quiet pyramid this way and that, as heat haze does.
func (p *painter) shimmer(b *pyramid) {
	x0, x1 := max(0, b.x0-3), min(p.w, b.x1+3)
	if x1-x0 < 2 {
		return
	}
	row := reuse(p.bufs.row, x1-x0)
	p.bufs.row = row
	for y := max(0, b.base-b.planned-3); y < b.base-1; y++ {
		dx := int(math.Round((noise(b.seed+9, float64(y)*.55+float64(p.frame)*.8) - .5) * 2.6))
		if dx == 0 {
			continue
		}
		copy(row, p.px[y*p.w+x0:y*p.w+x1])
		for x := x0; x < x1; x++ {
			sx := min(x1-1, max(x0, x-dx))
			p.px[y*p.w+x] = row[sx-x0]
		}
	}
}

// falling sends stones of a collapsed pyramid sliding down its faces into the rubble, and dust puffing up.
func (p *painter) falling(b *pyramid) {
	cut := b.collapse()
	for k := range 3 {
		t := float64((p.frame+k*3)%9) / 9
		c := int(float64(cut) * (1 - t))
		lo, hi := b.span(c)
		x, y := lo-1-int(t*2), b.base-1-c
		if k%2 == 1 {
			x = hi + 1 + int(t*2)
		}
		p.fill(x, y, p.pal.stone)
		p.blend(x, y+1, p.pal.dust, .5)
	}
	puff := .25 + .2*math.Sin(float64(p.frame)*.8)
	for x := b.x0 - 3; x < b.x1+3; x++ {
		for y := b.base - 3; y < b.base; y++ {
			p.blend(x, y, p.pal.dust, puff*unit(b.seed+12, x*13+y+p.frame))
		}
	}
}

// slide plays a kill: the pyramid crumbles, slumping lower and spreading wider into a heap of sand that sinks
// into the desert, grains streaming down its faces, nearly gone by the end.
func (p *painter) slide(b *pyramid) {
	d := b.dying
	height := float64(b.built) * (1 - .92*smooth(.02, .95, d))
	half := float64(b.x1-b.x0) / 2 * (1 + .9*smooth(.02, .9, d))
	fade := 1 - smooth(.7, 1, d)
	sandy := smooth(.05, .6, d)
	cx := b.cx()
	litWest, litEast := mix(p.pal.shade, p.pal.stone, 1-p.east), mix(p.pal.shade, p.pal.stone, p.east)
	for k := 0; float64(k) < height; k++ {
		y := b.base - 1 - k
		f := 1 - (float64(k)+.5)/math.Max(1, height)
		h := half * math.Pow(f, .7+.8*sandy)
		for x := int(cx - h - 1); x <= int(cx+h+1); x++ {
			fx := float64(x) + .5 - cx
			if math.Abs(fx) > h+.35 {
				continue
			}
			stone := litEast
			if fx < 0 {
				stone = litWest
			}
			c := mix(stone, mix(p.pal.sand, p.pal.sandShade, unit(b.seed+21, k*53+x)), sandy)
			if unit(b.seed+22, k*71+x+int(d*40)) < .25*sandy {
				continue
			}
			if p.in(x, y) {
				p.blend(x, y, c, fade)
				p.solid[y*p.w+x] = fade > .5
			}
		}
	}
	for g := range 10 {
		t := math.Mod(unit(b.seed+23, g)+float64(p.frame)*.17, 1)
		side := float64(1 - 2*(g%2))
		k := int(height * (1 - t))
		x := int(cx + side*(half*t+.5))
		p.blend(x, b.base-1-k, p.pal.sand, .9*fade)
	}
	ry := float64(b.planned)*.4*smooth(.05, .5, d) + 2
	for y := max(0, int(float64(b.base)-ry)); y < b.base+2; y++ {
		fy := float64(y) + .5
		for x := int(cx - half - 3); x <= int(cx+half+3); x++ {
			fx := float64(x) + .5
			n := fbm2(b.seed+24, fx*.15, fy*.3-float64(p.frame)*.4, 2)
			e := math.Hypot((fx-cx)/(half+3), (fy-float64(b.base))/ry)
			p.blend(x, y, mix(p.pal.dustShade, p.pal.dust, n), smooth(1, .4, e+(n-.5)*.6)*.6*fade)
		}
	}
}

// shaft is a soft beam of light from the sky onto the selected pyramid's apex.
func (p *painter) shaft(b *pyramid) {
	cx, apex := b.cx(), b.base-b.planned
	for y := range max(0, apex) {
		t := float64(y) / math.Max(1, float64(apex))
		half := .8 + 1.2*t
		for x := int(cx - half - 1); x <= int(cx+half+1); x++ {
			a := smooth(half+.8, half-.4, math.Abs(float64(x)+.5-cx)) * .26 * smooth(0, .6, t)
			p.blend(x, y, p.pal.rim, a)
		}
	}
}

// beaconGlow is the red light the needs-you beacon fire throws round itself while it is lit.
func (p *painter) beaconGlow(b *pyramid) {
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

// beaconAt is the cell the needs-you beacon burns in: at the head of the west ramp, or by the apex once the
// ramps are gone.
func (p *painter) beaconAt(b *pyramid) (int, int) {
	row := b.topRow() - 1
	if !b.ramps() {
		return b.mid() - 2, b.crestRow() - 1
	}
	return b.westAt(row + 1), row
}
