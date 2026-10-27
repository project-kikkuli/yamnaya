package forest

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// look is how a tree is drawn this frame: leaning east by sway pixels at its crown in the wind, burning at
// frame burn (or not, -1), turned theta radians toward dir as it is felled, and alpha of it shown.
type look struct {
	sway, theta, dir, alpha float64
	burn                    int
}

var upright = look{dir: 1, alpha: 1, burn: -1}

// shade is the tree's color at u pixels east of the trunk's middle and v pixels above its roots, ok false off it.
func (p *painter) shade(t *tree, u, v float64, lk look) (rgb, bool) {
	h := float64(t.h)
	if v < 0 || v >= h {
		return rgb{}, false
	}
	bend := lk.sway * (v / h) * (v / h)
	if c, ok := p.crown(t, u-bend, v, lk); ok {
		return c, true
	}
	reach := float64(t.tw) + .5
	if v < 1.5 && t.h >= 10 {
		reach += .8
	}
	top := t.vc
	if t.style == conifer {
		top = h * .92
	}
	if v >= top || math.Abs(u-bend*.6) > reach {
		return rgb{}, false
	}
	if lk.burn >= 0 {
		if unit(t.seed+11, int(v)*31+lk.burn) < .12 {
			return p.pal.fire, true
		}
		return p.pal.char, true
	}
	c := p.pal.bark
	switch {
	case t.style == birch:
		c = p.pal.birch
		if unit(t.seed+12, int(v)) < .3 {
			c = p.pal.barkShade
		}
	case t.tw > 0 && u-bend*.6 > .4:
		c = p.pal.barkShade
	case t.tw > 0 && u-bend*.6 < -.4:
		c = mix(p.pal.bark, p.pal.leafLit, .2)
	}
	return c, true
}

// crown is the tree's leaves at u, v: a rounded crown on a broadleaf or birch, stacked tiers on a conifer, in
// blossom once it is done, and on fire while it burns.
func (p *painter) crown(t *tree, u, v float64, lk look) (rgb, bool) {
	h := float64(t.h)
	n := fbm2(t.seed, u*.45+3, v*.45+7, 2)
	var light float64
	leaf := p.pal.leaf
	if t.style == conifer {
		v0 := h * .16
		if v < v0 {
			return rgb{}, false
		}
		tier := math.Max(3, h/6)
		saw := math.Mod(h-v, tier) / tier
		half := t.rx*(h-v)/(h-v0)*(.72+.38*saw) + .4
		if math.Abs(u) > half {
			return rgb{}, false
		}
		light = clamp(.55-u/half*.35+(saw-.5)*.3+(n-.5)*.6, 0, 1)
		leaf = p.pal.needle
	} else {
		d := math.Hypot(u/t.rx, (v-t.vc)/t.ry)
		if d+(n-.5)*.55 >= 1 {
			return rgb{}, false
		}
		light = clamp(.55-u/t.rx*.3+(v-t.vc)/t.ry*.3+(n-.5)*.9, 0, 1)
	}
	if lk.burn >= 0 {
		f := fbm2(t.seed+9, u*.5, v*.4-float64(lk.burn)*.7, 2) + .15*(v/h-.5)
		switch {
		case f > .66:
			return p.pal.fireHot, true
		case f > .55:
			return p.pal.fire, true
		case f > .47:
			return mix(p.pal.fire, p.pal.char, .6), true
		}
		return mix(p.pal.char, p.pal.leafShade, .5*n), true
	}
	c := mix(p.pal.leafShade, leaf, smooth(0, .6, light))
	c = mix(c, p.pal.leafLit, smooth(.55, 1, light))
	if t.weather == scene.Summit {
		switch k := spread(t.seed, int(math.Floor(u))*131+int(math.Floor(v))) % 100; {
		case k < 10:
			c = p.pal.blossomHot
		case k < 40:
			c = mix(p.pal.blossom, c, .15*(1-light))
		}
	}
	return c, true
}

// tree paints t as lk says: upright over its own bounds, or turned about its roots as it falls.
func (p *painter) tree(t *tree, lk look) {
	put := func(x, y int, c rgb) {
		if lk.alpha >= 1 {
			p.fill(x, y, c)
		} else {
			p.blend(x, y, c, lk.alpha)
		}
	}
	if lk.theta == 0 {
		reach := int(math.Ceil(t.rx+math.Abs(lk.sway))) + 1
		for y := max(0, t.base-t.h); y < min(p.p, t.base); y++ {
			v := float64(t.base - 1 - y)
			for x := max(0, t.cx-reach); x <= min(p.w-1, t.cx+reach); x++ {
				if c, ok := p.shade(t, float64(x-t.cx), v, lk); ok {
					put(x, y, c)
				}
			}
		}
		return
	}
	sin, cos := math.Sincos(lk.theta)
	r := t.h + int(t.rx) + 2
	x0, x1 := t.cx-r, t.cx+r
	if lk.dir > 0 {
		x0 = t.cx - int(t.rx) - 2
	} else {
		x1 = t.cx + int(t.rx) + 2
	}
	for y := max(0, t.base-r); y <= min(p.p-1, t.base); y++ {
		V := float64(t.base - 1 - y)
		for x := max(0, x0); x <= min(p.w-1, x1); x++ {
			X := float64(x - t.cx)
			u, v := X*cos-lk.dir*V*sin, lk.dir*X*sin+V*cos
			if c, ok := p.shade(t, u, v, lk); ok {
				put(x, y, c)
			}
		}
	}
}
