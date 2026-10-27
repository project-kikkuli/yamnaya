package forest

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// motion paints what moves over a copy of the backdrop: the forest's own life, trees swaying, burning or being
// felled, lightning, petals, the light on the selection and the glow of lanterns and nests, then the glyphs.
func (p *painter) motion() {
	p.ambient()
	for i := range p.l.ts {
		t := &p.l.ts[i]
		switch {
		case t.dying > 0:
			p.felled(t)
			continue
		case t.weather == scene.Storm:
			p.tree(t, look{sway: 1.8 * math.Sin(float64(p.frame)*.9+float64(t.seed%7)), dir: 1, alpha: 1, burn: -1})
			p.bolt(t)
		case t.weather == scene.Eruption:
			p.tree(t, look{dir: 1, alpha: 1, burn: p.frame})
			p.embers(t)
		case t.weather == scene.Summit:
			p.petals(t)
		}
		if t.selected {
			p.sunbeam(t)
		}
		if t.needsYou {
			p.lanternGlow(t)
		}
		p.nestGlow(t)
	}
	p.claimTrees()
	p.sprites()
}

// claimTrees marks every cell a standing tree fills as its own, so a click there selects it.
func (p *painter) claimTrees() {
	for i := range p.l.ts {
		t := &p.l.ts[i]
		reach := int(math.Ceil(t.rx))
		for y := max(0, t.top()); y <= t.ground(); y++ {
			for x := max(0, t.cx-reach); x <= min(p.w-1, t.cx+reach); x++ {
				p.who[y*p.w+x] = i + 1
			}
		}
	}
}

// ambient is the forest's own life: stars twinkle on the slow beat, and at night fireflies drift over the grass.
func (p *painter) ambient() {
	beat := p.frame / int(scene.Ambient/scene.Step)
	for _, s := range p.stars {
		b := s.b * (.45 + .55*unit(0x57a7, s.i*31+beat))
		p.px[s.i] = mix(p.px[s.i], white, b*p.pal.stars)
	}
	glint := mix(p.pal.skyAt(.97), p.pal.firefly, .3*p.pal.lights)
	for x := range p.w {
		if spread(0x91e, x*7+beat)%13 == 0 {
			y := p.brookAt(x) + int(spread(0x91f, x+beat)%2)
			p.blend(x, y, glint, .6)
		}
	}
	if p.pal.lights < .5 {
		return
	}
	glow := (p.pal.lights - .5) * 2
	for k := range p.w / 8 {
		f := float64(p.frame) * .25
		x := int(float64(p.w) * math.Mod(unit(0xff1, k)+f*.004*(unit(0xff2, k)-.5)+1, 1))
		y := p.hz - 2 - int(10*unit(0xff3, k)) + int(math.Round(2*math.Sin(f*.7+float64(k))))
		if math.Sin(f*1.3+float64(k)*2.1) < -.2 {
			continue
		}
		p.blend(x, y, p.pal.firefly, .9*glow)
		p.blend(x-1, y, p.pal.firefly, .25*glow)
		p.blend(x+1, y, p.pal.firefly, .25*glow)
	}
}

// felled is the kill: the tree topples over about its roots, faster as it goes, lands with a burst of leaves and
// dust, and fades away as the kill ends.
func (p *painter) felled(t *tree) {
	theta, dir := t.fell()
	alpha := 1 - smooth(.72, 1, t.dying)
	p.tree(t, look{theta: theta, dir: dir, alpha: alpha, burn: -1})
	if t.dying < .12 {
		for k := range 4 {
			x := t.cx + int(dir)*(t.tw+1+k%2)
			p.blend(x, t.base-1-k/2, p.pal.birch, .8)
		}
	}
	burst := smooth(.6, .75, t.dying) * (1 - smooth(.8, 1, t.dying))
	if burst <= 0 {
		return
	}
	land := float64(t.cx) + dir*float64(t.h)*.7
	for k := range 24 {
		x := land + (unit(t.seed+21, k)-.5)*float64(t.h)*.9
		y := float64(t.base) - 1 - unit(t.seed+22, k)*6*burst
		c := p.pal.leaf
		if k%3 == 0 {
			c = p.pal.litter
		}
		p.blend(int(x), int(y), c, .8*burst)
	}
}

// bolt strikes now and then from a storm's cloud down into the crown.
func (p *painter) bolt(t *tree) {
	if (p.frame/4+int(t.seed%97))%5 != 0 || p.frame%4 > 1 {
		return
	}
	cx, cy, _, ry := t.stormShape()
	x, crown := int(cx)+int(spread(t.seed, p.frame/4)%3)-1, t.base-t.h
	for y := int(cy + ry*.6); y < crown; y++ {
		p.set(x, y, p.pal.bolt)
		p.blend(x-1, y, p.pal.bolt, .35)
		p.blend(x+1, y, p.pal.bolt, .35)
		if spread(t.seed+5, y*13+p.frame)%3 == 0 {
			x += int(spread(t.seed+6, y+p.frame)%3) - 1
		}
	}
}

// embers rise from a burning crown into its smoke.
func (p *painter) embers(t *tree) {
	top := float64(t.base - t.h)
	for k := range 10 {
		rise := math.Mod(float64(p.frame)*(.8+unit(t.seed+31, k))+unit(t.seed+32, k)*20, 20)
		x := float64(t.cx) + (unit(t.seed+33, k)-.5)*t.rx*2 + math.Sin(rise*.5+float64(k))
		c := p.pal.fireHot
		if k%2 == 0 {
			c = p.pal.fire
		}
		p.blend(int(x), int(top+t.ry-rise), c, .9*(1-rise/20))
	}
}

// petals drift down from a tree in full bloom.
func (p *painter) petals(t *tree) {
	top, fall := float64(t.base)-t.vc-t.ry*.5, t.vc+t.ry*.5+2
	for k := range 8 {
		d := math.Mod(float64(p.frame)*(.5+.5*unit(t.seed+51, k))+unit(t.seed+52, k)*fall, fall)
		x := float64(t.cx) + (unit(t.seed+53, k)-.5)*t.rx*2.4 + 1.5*math.Sin(d*.4+float64(k))
		p.blend(int(x), int(top+d), p.pal.blossomHot, .85)
	}
}

// sunbeam lets a shaft of light down through the canopy onto the selected tree.
func (p *painter) sunbeam(t *tree) {
	cx, tip := float64(t.cx)+.5, float64(t.base-t.h)
	for y := 0; y < t.base; y++ {
		fy := float64(y) + .5
		half := t.rx*.5 + 1 + fy/float64(t.base)*t.rx*.5
		mid := cx + (tip-fy)*.18
		a := .26 * smooth(0, tip*.5+1, fy) * (1 - .5*smooth(tip, float64(t.base), fy))
		for x := int(mid - half - 1); x <= int(mid+half+1); x++ {
			p.blend(x, y, p.pal.beam, a*smooth(half+.8, half-.6, math.Abs(float64(x)+.5-mid)))
		}
	}
}

// lanternGlow is the red light the needs-you lantern throws round itself while it is lit.
func (p *painter) lanternGlow(t *tree) {
	if p.frame/4%2 != 0 || t.dying > 0 {
		return
	}
	x, row := t.lantern()
	y := 2*row + 1
	for dy := -3; dy <= 3; dy++ {
		for dx := -3; dx <= 3; dx++ {
			p.blend(x+dx, y+dy, p.pal.red, .4*math.Exp(-float64(dx*dx+dy*dy)/3))
		}
	}
}

// nestGlow is the soft green a nest gives off while its CI passes, brighter after dark.
func (p *painter) nestGlow(t *tree) {
	x := t.cx + t.tw + 1
	for _, n := range t.nests {
		if n.state != scene.Green {
			continue
		}
		y := 2*n.row + 1
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				p.blend(x+dx, y+dy, p.pal.green, (.2+.25*p.pal.lights)*math.Exp(-float64(dx*dx+dy*dy)/2))
			}
		}
	}
}

// lantern is the cell the needs-you lantern hangs in: from a branch on the east of the upper crown.
func (t *tree) lantern() (int, int) {
	crown := t.base - int(t.vc+t.ry)
	if t.style == conifer {
		crown = t.base - int(float64(t.h)*.75)
	}
	return t.cx + max(t.tw+2, int(t.rx*.6)), max(t.top()+1, crown/2+1)
}
