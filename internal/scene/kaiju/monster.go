package kaiju

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// span is how wide the monster stands, in pixels.
func (m *monster) span() float64 { return m.w }

// sprite is the monster's art in the pose it beats into at a beat.
func (m *monster) sprite(beat int) *sprite {
	if m.young {
		return m.kind.young
	}
	return m.kind.poses[beat&1]
}

// mouth is where its breath leaves it.
func (m *monster) mouth() (float64, float64) {
	sp := m.sprite(0)
	fx := sp.mx
	if m.facing < 0 {
		fx = 1 - fx
	}
	return m.x - m.w/2 + fx*m.w, m.top() + sp.my*m.h
}

func (p *painter) flash(m *monster) bool { return (p.frame+int(unit(m.seed, 12)*16))%16 < 2 }

// monster draws monster i as a silhouette against the lights in the way its kind moves: stomping a pixel up and
// down, beating its wings, swaying its coils, scuttling, or shaking like a handheld camera on it; cut off where
// the bay reaches up its body, on its ruined tower on a summit, glowing when it breathes fire. A killed monster
// sinks back under the sea, or a machine buckles and falls into its own dust.
func (p *painter) monster(i int) {
	m := &p.l.monsters[i]
	kd := m.kind
	sp := m.sprite(p.frame / 2)
	h, w := m.h, m.span()
	foot, cut := m.foot, m.water
	d := m.dying
	squash, tilt, bob, nudge := 1.0, 0.0, 0.0, 0.0
	if d == 0 {
		if (p.frame/2+int(m.seed&1))%2 == 1 {
			bob = 1
		}
		if kd.fly > 0 && !m.young {
			bob = 2 * math.Sin(p.t*.8+float64(m.seed&7))
		}
		if kd.crawls || m.young {
			nudge = 2 * math.Sin(p.t*.4+float64(m.seed&7))
		}
		if kd.shaky {
			nudge, bob = math.Round(2*unit(m.seed, p.frame)-1), math.Round(2*unit(m.seed+1, p.frame)-1)
		}
	}
	switch {
	case d > 0 && kd.steel:
		squash, tilt = 1-.9*d, .9*d*m.facing
	case d > 0:
		foot += d * .3 * h
		cut = lerp(math.Min(cut, foot+1), foot-h-2, d)
	}
	if m.weather == scene.Summit && d == 0 {
		p.ruin(m, int32(i+1))
	}
	hh := h * squash
	top := foot - hh - bob
	left := m.x - w/2 + nudge
	pal := p.pal
	flash := m.weather == scene.Storm && p.flash(m) && d == 0
	glow := .6 + .4*math.Sin(p.t*1.3+float64(m.seed&7))
	erupts := m.weather == scene.Eruption && d == 0
	who := int32(i + 1)
	for py := range int(math.Ceil(hh)) {
		y := int(top) + py
		if float64(y) >= cut {
			break
		}
		sy := min(sp.h-1, int(float64(py)/hh*float64(sp.h)))
		shift := tilt*(hh-float64(py))*.6 + kd.sway*math.Sin(float64(py)*.35-p.t*.5)
		for px := range int(math.Ceil(w)) {
			sx := min(sp.w-1, int(float64(px)/w*float64(sp.w)))
			if m.facing < 0 {
				sx = sp.w - 1 - sx
			}
			ch := sp.px[sy*sp.w+sx]
			if ch == '.' {
				continue
			}
			x := int(left + float64(px) + shift)
			if !p.in(x, y) {
				continue
			}
			edge := sp.edge[sy*sp.w+sx]
			v := pal.body + .05*(1-float64(py)/hh)
			if kd.gold {
				v += .07
				p.color(x, y, pal.gold, .16)
			}
			if edge {
				v = lerp(v, pal.rim, .55)
			}
			switch ch {
			case 'e':
				v = .97
			case 'l':
				v = .9
				if erupts {
					p.color(x, y, pal.breath, .7*glow)
				}
			case 'w':
				v = lerp(pal.body, pal.rim, .3+.15*float64((sx+sy)&1))
			case 's':
				v += .1
				if erupts {
					v = .55 + .4*glow
					p.color(x, y, pal.breath, .75)
				}
			}
			if flash {
				v = math.Max(v, .25)
				if edge {
					v = .98
				}
			}
			k := y*p.w + x
			p.lum[k], p.solid[k] = float32(v), true
			p.who[y/2*p.w+x] = who
		}
	}
	if cut < foot {
		p.wake(m, cut, w, d)
	}
	if d > 0 && kd.steel {
		p.dust(m, foot, w, d)
	}
	if kd.shaky && d == 0 {
		p.parasites(m)
	}
}

// parasites drop off the lanky beast and scurry along the ground under it.
func (p *painter) parasites(m *monster) {
	for n := range 5 {
		age := math.Mod(p.t*.6+float64(n)*2.3, 8)
		x := m.x + (unit(m.seed, 40+n)-.5)*m.w + age*float64(1-2*(n%2))
		y := m.top() + m.h*.4 + math.Min(age*age*1.5, m.h*.6)
		p.put(int(x), int(y), .55)
	}
}

// wake is the foam where the bay meets the monster's body, rings rolling out from it as it wades.
func (p *painter) wake(m *monster, cut, w, d float64) {
	y := int(cut)
	reach := w*.6 + 2 + d*w
	for x := int(m.x - reach); x <= int(m.x+reach); x++ {
		off := math.Abs(float64(x)+.5-m.x) / reach
		foam := (.35 + .2*math.Sin(float64(x)*.9+p.t*1.1)) * (1 - off*off)
		p.toward(x, y, .95, foam+d*.4)
		p.toward(x, y+1, .8, foam*.5)
	}
	ring := math.Mod(p.t*.8+unit(m.seed, 5)*9, 9)
	for _, side := range [2]float64{-1, 1} {
		x := int(m.x + side*(w*.55+ring))
		for dx := -1; dx <= 1; dx++ {
			p.toward(x+dx, y+1, .9, .45*(1-ring/9))
		}
	}
}

// dust billows round a fallen machine's feet and settles as its kill ends.
func (p *painter) dust(m *monster, foot, w, d float64) {
	r := 3 + d*w*.7
	fade := 1 - smooth(.7, 1, d)
	for y := int(foot - r); y <= int(foot+1); y++ {
		for x := int(m.x - r*1.4); x <= int(m.x+r*1.4); x++ {
			fx, fy := float64(x)+.5, float64(y)+.5
			e := math.Hypot((fx-m.x)/1.4, fy-foot) / r
			a := smooth(1, .4, e) * smooth(.35, .6, scene.Fbm2(m.seed, fx*.25, fy*.3-p.t*.2, 2))
			p.toward(x, y, p.pal.smoke+.15, a*fade*.9)
		}
	}
}

// ruin is the broken lattice tower a monster on a summit stands on top of.
func (p *painter) ruin(m *monster, who int32) {
	w := m.span()
	for y := int(m.foot); y < int(m.base); y++ {
		f := (float64(y) - m.foot) / math.Max(1, m.base-m.foot)
		half := int(w * (.18 + .2*f))
		cx := int(m.x)
		for x := cx - half; x <= cx+half; x++ {
			if (x == cx-half || x == cx+half || (x-cx+y)%3 == 0 || (cx-x+y)%3 == 0) && unit(m.seed, y*131+x) > .12 {
				k := y*p.w + x
				if p.in(x, y) {
					p.lum[k], p.solid[k] = float32(p.pal.city+.12), true
					p.who[y/2*p.w+x] = who
				}
			}
		}
	}
}

// beam is a searchlight from x0, y0 toward x1, y1, widening and fading as it goes, a of full strength.
func (p *painter) beam(x0, y0, x1, y1, spread, a float64) {
	n := int(math.Hypot(x1-x0, y1-y0))
	if n == 0 || a <= 0 {
		return
	}
	for s := range n {
		t := float64(s) / float64(n)
		x, y := x0+(x1-x0)*t, y0+(y1-y0)*t
		half := .6 + spread*float64(s)
		fall := a * (1 - .55*t)
		for dx := int(-half); dx <= int(half); dx++ {
			p.add(int(x)+dx, int(y), fall*(1-math.Abs(float64(dx))/(half+1)))
		}
	}
}

// searchlights sweep the sky from the quay, slow and out of step.
func (p *painter) searchlights() {
	n := max(2, p.w/45)
	g := p.l.ground
	for i := range n {
		x := (float64(i) + .5) * float64(p.w) / float64(n)
		th := -math.Pi/2 + .6*math.Sin(p.t*.06+float64(i)*1.9)
		reach := (g - p.l.top) * 1.2
		p.beam(x, g, x+math.Cos(th)*reach*1.6, g+math.Sin(th)*reach, .06, p.pal.beam*.55)
	}
}

// fixed is every beam a defending crew holds on monster i: one per busy agent, and two crossing on it when it is
// selected.
func (p *painter) fixed(i int) {
	m := &p.l.monsters[i]
	if m.dying > 0 {
		return
	}
	g := p.l.ground
	hx, hy := m.x, m.top()+m.h*.3
	a := math.Max(.1, p.pal.beam*.7)
	j := 0
	for _, c := range m.team {
		if c.Stance != scene.Climbing {
			continue
		}
		side := float64(1 - 2*(j%2))
		sx := m.x + side*(m.span()*.7+5+float64(j/2)*4)
		p.beam(sx, g, hx+2*math.Sin(p.t*.25+float64(j)), hy, .05, a)
		j++
	}
	if m.selected {
		for _, side := range [2]float64{-1, 1} {
			p.beam(m.x+side*(m.span()+14), g, hx, hy, .07, math.Max(.18, p.pal.beam*1.3))
		}
	}
}

// weather plays monster i's weather over it: smoke drifting across it, a storm cloud with lightning, its atomic
// breath sweeping the city into flames, or the roar it lets out from its tower.
func (p *painter) weather(i int) {
	m := &p.l.monsters[i]
	if m.dying > 0 {
		return
	}
	switch m.weather {
	case scene.Clouds:
		p.smoke(m, m.x, m.top()+m.h*.35, m.span()*1.1+3, .8)
	case scene.Storm:
		p.storm(m)
	case scene.Eruption:
		p.breath(m)
	case scene.Summit:
		p.roar(m)
	}
}

func (p *painter) smoke(m *monster, cx, cy, r, a float64) {
	drift := p.t * .3
	for _, px := range p.clouds[cloudShape{cx, cy, r, false}] {
		s := smooth(.4, .68, scene.Noise2(m.seed+31, (px.x-drift)*.12, px.y*.2))
		if alpha := s * px.envelope * a; alpha > 0 {
			p.lum[px.k] += float32(alpha) * (float32(p.pal.smoke) - p.lum[px.k])
		}
	}
}

func (p *painter) storm(m *monster) {
	c := stormShape(m, p.l.top)
	for _, px := range p.clouds[c] {
		s := smooth(.34, .62, scene.Noise2(m.seed+41, (px.x-p.t*.15)*.15, px.y*.25))
		if alpha := s * px.envelope * .85; alpha > 0 {
			p.lum[px.k] += float32(alpha) * (float32(.02) - p.lum[px.k])
		}
	}
	if !p.flash(m) {
		return
	}
	x := c.cx + (unit(m.seed, p.frame/16)-.5)*m.span()
	for y := int(c.cy); y < int(m.top()+m.h*.3); y++ {
		x += (unit(m.seed+3, y+p.frame) - .5) * 2.4
		p.put(int(x), y, 1)
		p.add(int(x)+1, y, .3)
	}
}

// breath is the atomic ray: every few seconds a white-blue beam from the jaw down into the city, which bursts
// into flame where it lands and smokes after.
func (p *painter) breath(m *monster) {
	mx, my := m.mouth()
	tx, ty := mx+m.facing*(m.h*.6+6), math.Max(my+4, p.l.ground-1)
	p.smoke(m, tx, ty-6, 6, .7)
	f := (p.frame + int(m.seed%12)) % 12
	burn := .5 + .5*math.Sin(p.t*1.7)
	for y := int(ty - 3); y <= int(ty+1); y++ {
		for x := int(tx - 4); x <= int(tx+4); x++ {
			e := math.Hypot(float64(x)+.5-tx, (float64(y)+.5-ty)*1.6) / 4
			if e < 1 {
				p.add(x, y, .5*(1-e))
				p.color(x, y, mix(p.pal.fire, p.pal.fireHot, burn*(1-e)), .85*(1-e*e))
			}
		}
	}
	if f < 4 || f >= 9 {
		return
	}
	n := int(math.Hypot(tx-mx, ty-my))
	for s := range n {
		t := float64(s) / float64(n)
		x, y := int(mx+(tx-mx)*t), int(my+(ty-my)*t)
		p.put(x, y, 1)
		p.color(x, y, p.pal.breath, .35)
		for _, dy := range [2]int{-1, 1} {
			p.toward(x, y+dy, .8, .35)
		}
	}
	r := 3 + float64(f-4)*1.4
	for y := int(ty - r); y <= int(ty+r*.5); y++ {
		for x := int(tx - r); x <= int(tx+r); x++ {
			e := math.Hypot(float64(x)+.5-tx, float64(y)+.5-ty) / r
			if e < 1 {
				p.toward(x, y, 1, 1-e)
				p.color(x, y, mix(p.pal.fireHot, p.pal.fire, e), .9*(1-e))
			}
		}
	}
}

// roar rolls rings of sound out of the jaw of a monster on its tower.
func (p *painter) roar(m *monster) {
	mx, my := m.mouth()
	for j := range 3 {
		r := math.Mod(p.t*1.5+float64(j)*4, 12) + 2
		for a := -.7; a <= .7; a += .7 / r {
			x := mx + m.facing*math.Cos(a)*r
			if m.kind == robot {
				x = mx + math.Cos(a)*r*float64(1-2*(j%2))
			}
			p.toward(int(x), int(my+math.Sin(a)*r*.8), .95, .6*(1-r/14))
		}
	}
}
