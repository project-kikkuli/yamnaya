package kaiju

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// backdrop is the set as it holds still, in greys before the film prints them: the night sky and moon, the far
// city, each district's low blocks, pagodas and pylons, the tower, the quay and the bay. Frames add what moves.
type backdrop struct {
	pal  palette
	ink  *[256]rgb
	w, p int
	l    layout
	lum  []float32
	// vig is how much the lens darkens each pixel toward the corners; the letterbox is 0.
	vig []float32
	// phase is each water pixel's place in the swell, 0..31; dry pixels are noSwell.
	phase []uint8
	// solid is what a terminal with no colors draws as a block: the city and the quay.
	solid []bool
	// glint is the water under the moon that sparkles.
	glint        []int32
	moonX, moonY float64
	clouds       map[cloudShape][]cloudPixel
}

const noSwell = 255

func build(pal palette, l layout, w, rows int) *backdrop {
	p := rows * 2
	b := &backdrop{pal: pal, ink: pal.print(), w: w, p: p, l: l, lum: make([]float32, w*p), vig: make([]float32, w*p),
		phase: make([]uint8, w*p), solid: make([]bool, w*p)}
	for k := range b.phase {
		b.phase[k] = noSwell
	}
	b.moonX, b.moonY = float64(w)*.8, l.top+(l.ground-l.top)*.2
	b.sky()
	b.skyline()
	b.harbor()
	cx, cy := float64(w)/2, (l.top+l.bottom)/2
	for y := int(l.top); y < int(l.bottom); y++ {
		for x := range w {
			r := math.Hypot((float64(x)+.5-cx)/(float64(w)/2), (float64(y)+.5-cy)/((l.bottom-l.top)/2))
			b.vig[y*w+x] = float32(1 - .42*smooth(.55, 1.15, r))
		}
	}
	return b
}

func (b *backdrop) set(x, y int, v float64) {
	if x >= 0 && x < b.w && y >= int(b.l.top) && y < int(b.l.bottom) {
		b.lum[y*b.w+x] = float32(v)
	}
}

func (b *backdrop) wall(x, y int, v float64) {
	if x >= 0 && x < b.w && y >= int(b.l.top) && y < int(b.l.bottom) {
		b.lum[y*b.w+x], b.solid[y*b.w+x] = float32(v), true
	}
}

// sky darkens from the horizon's haze up to the zenith, with thin cloud and the moon in its halo.
func (b *backdrop) sky() {
	pal, l := b.pal, b.l
	r := math.Max(2, (l.bottom-l.top)*.045)
	for y := int(l.top); y < int(l.ground); y++ {
		f := math.Pow((float64(y)-l.top)/(l.ground-l.top), 1.6)
		for x := range b.w {
			fx, fy := float64(x)+.5, float64(y)+.5
			v := lerp(pal.zenith, pal.horizon, f)
			v += .05 * smooth(.5, .75, scene.Fbm2(0xc10d, fx*.03, fy*.11, 3))
			d := math.Hypot(fx-b.moonX, (fy-b.moonY)*1.1)
			v += .22 * math.Exp(-d*d/(r*r*14)) * (1 - pal.zenith)
			if d < r {
				v = pal.moon - .12*scene.Noise2(0x300, fx*.6, fy*.6)
			}
			b.lum[y*b.w+x] = float32(v)
		}
	}
}

// skyline raises the city on the ground line: a hazy far city, then each district's own blocks, pagodas and
// power pylons strung with wires, and the tower over it all.
func (b *backdrop) skyline() {
	pal, l := b.pal, b.l
	h := l.bottom - l.top
	g := int(l.ground)
	for x := range b.w {
		fh := (.05 + .1*scene.Noise(0xfa4, float64(x/4)*.6)) * h
		for y := g - int(fh); y < g; y++ {
			v := pal.far
			if unit(0xf1, y*b.w+x) < .05 && y%2 == 0 {
				v = lerp(pal.far, pal.window, .6)
			}
			b.wall(x, y, v)
		}
	}
	var tops [][2]int
	for x, k := 0, 0; x < b.w; k++ {
		seed := uint64(0x5eed)
		if d := l.group(float64(x)); d >= 0 {
			seed = l.districts[d].seed
		}
		u := unit(seed, k*7+x)
		switch {
		case u < .48:
			w := 4 + int(unit(seed, k*7+x+1)*6)
			b.block(x, g, w, (.035+.07*unit(seed, k*7+x+2))*h, seed)
			x += w + int(unit(seed, k*7+x+3)*2)
		case u < .7:
			w := 7 + 2*int(unit(seed, k*7+x+1)*2)
			b.pagoda(x+w/2, g, w, (.09+.07*unit(seed, k*7+x+2))*h)
			x += w + 1
		case u < .84:
			ph := (.15 + .04*unit(seed, k*7+x+2)) * h
			b.pylon(x+2, g, ph)
			tops = append(tops, [2]int{x + 2, g - int(ph)})
			x += 6
		default:
			b.block(x, g, 3, .025*h, seed)
			x += 4
		}
	}
	for i := 1; i < len(tops); i++ {
		a, c := tops[i-1], tops[i]
		if c[0]-a[0] > 40 {
			continue
		}
		for x := a[0] + 1; x < c[0]; x++ {
			t := float64(x-a[0]) / float64(c[0]-a[0])
			y := lerp(float64(a[1]), float64(c[1]), t) + 4*t*(1-t)*2 + 2
			b.set(x, int(y), lerp(pal.city, pal.horizon, .5)+.08)
		}
	}
	b.tower(int(float64(b.w)*.64), g, .42*h)
}

func (b *backdrop) block(x0, g, w int, h float64, seed uint64) {
	top := g - int(math.Max(2, h))
	shade := b.pal.city + .025*unit(seed, x0)
	for y := top; y < g; y++ {
		for x := x0; x < x0+w; x++ {
			v := shade
			if y == top {
				v += .05
			}
			if y > top && x > x0 && x < x0+w-1 && (y-top)%3 == 1 && unit(seed, y*997+x) < .35 {
				v = b.pal.window * (.7 + .3*unit(seed, y*31+x))
			}
			b.wall(x, y, v)
		}
	}
}

// pagoda stacks tiers on a narrowing body, each under an eave whose tips curl up, with a spire on top.
func (b *backdrop) pagoda(cx, g, w int, h float64) {
	tiers := 3
	if h > 10 {
		tiers = 4
	}
	th := h / float64(tiers)
	for t := range tiers {
		y0 := g - int(float64(t+1)*th)
		y1 := g - int(float64(t)*th)
		half := max(1, (w-2*t)/2-1)
		for y := y0 + 1; y < y1; y++ {
			for x := cx - half + 1; x <= cx+half-1; x++ {
				v := b.pal.city
				if (y-y0)%2 == 0 && x == cx && b.pal.window > .5 {
					v = b.pal.window * .8
				}
				b.wall(x, y, v)
			}
		}
		for x := cx - half - 1; x <= cx+half+1; x++ {
			b.wall(x, y0+1, b.pal.city+.04)
		}
		b.wall(cx-half-2, y0, b.pal.city+.04)
		b.wall(cx+half+2, y0, b.pal.city+.04)
		for x := cx - half; x <= cx+half; x++ {
			b.wall(x, y0, b.pal.city+.02)
		}
	}
	for y := g - int(h) - 3; y < g-int(h); y++ {
		b.wall(cx, y, b.pal.city+.05)
	}
}

// pylon is a lattice tower of the power lines: two legs leaning in, braced, a crossarm near the top.
func (b *backdrop) pylon(cx, g int, h float64) {
	top := g - int(h)
	for y := top; y < g; y++ {
		f := float64(y-top) / h
		half := int(.5 + 2.5*f)
		v := b.pal.city + .04
		b.wall(cx-half, y, v)
		b.wall(cx+half, y, v)
		if (y-top)%3 == 0 {
			for x := cx - half; x <= cx+half; x++ {
				b.wall(x, y, v)
			}
		}
	}
	for x := cx - 3; x <= cx+3; x++ {
		b.wall(x, top+2, b.pal.city+.04)
	}
}

// tower is the city's lattice broadcast tower: splayed legs, a deck two thirds up, a mast with a light on top.
func (b *backdrop) tower(cx, g int, h float64) {
	top := g - int(h)
	deck := g - int(h*.62)
	for y := top; y < g; y++ {
		f := float64(y-top) / h
		half := 1 + int(h*.16*math.Pow(f, 2.2))
		v := b.pal.city + .05
		if y < top+int(h*.18) {
			b.wall(cx, y, v)
			continue
		}
		for x := cx - half; x <= cx+half; x++ {
			edge := x == cx-half || x == cx+half
			lattice := (x-cx+y)%4 == 0 || (cx-x+y)%4 == 0
			if edge || lattice || y == deck || y == deck+1 || half <= 1 {
				b.wall(x, y, v)
			}
		}
		if y > deck+1 && y < g && half > 3 && y > g-int(h*.12) {
			for x := cx - half/3; x <= cx+half/3; x++ {
				b.set(x, y, lerp(b.pal.city, b.pal.horizon, .25))
			}
		}
	}
	for x := cx - 3; x <= cx+3; x++ {
		b.wall(x, deck, b.pal.city+.08)
		if b.pal.window > .5 && x%2 == 0 {
			b.wall(x, deck+1, b.pal.window)
		}
	}
	b.wall(cx, top, b.pal.moon)
}

// harbor lays the quay along the ground line, its lamps, and the bay out to the letterbox with its swell and the
// moon's path on it.
func (b *backdrop) harbor() {
	pal, l := b.pal, b.l
	g, q := int(l.ground), int(l.quay)
	for y := g; y < q; y++ {
		for x := range b.w {
			v := pal.quay + .02*scene.Noise2(0x9a, float64(x)*.3, float64(y)*.5)
			if y == g {
				v += .06
			}
			if y == q-1 {
				v -= .03
			}
			if y == g && x%9 == 4 && pal.window > .5 {
				v = pal.window
			}
			b.wall(x, y, v)
		}
	}
	for y := q; y < int(l.bottom); y++ {
		f := math.Pow((float64(y)-l.quay)/(l.bottom-l.quay), .7)
		for x := range b.w {
			fx, fy := float64(x)+.5, float64(y)+.5
			k := y*b.w + x
			v := lerp(pal.shore, pal.deep, f) + .05*(scene.Noise2(0xba1, fx*.04, fy*.8)-.5)
			if y == q {
				v += .08
			}
			b.lum[k] = float32(v)
			b.phase[k] = uint8(int(fx*.4+fy*2.3+14*scene.Noise2(0x77, fx*.06, fy*.3)) & 31)
			spread := 1.5 + (fy-l.quay)*.35
			if math.Abs(fx-b.moonX) < spread && unit(0x6c, k) < .45 {
				b.glint = append(b.glint, int32(k))
			}
		}
	}
}
