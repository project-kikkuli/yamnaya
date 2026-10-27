package islands

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// backdrop is everything that holds still while the layout's shape does: the sea, its shallows and lagoons, each
// island's beach, green and hills, and standing weather; plus what the frames animate, listed per island.
type backdrop struct {
	pal  palette
	w, p int
	px   []rgb
	// phase is each water pixel's place in the swell, 0..31; land is noSwell.
	phase []uint8
	// field is the height of the nearest island at each pixel, below zero in the water; own is its index or -1.
	field []float32
	own   []int16
	fx    []isleFx
	// waves are the open-water cells a wave crest may cross.
	waves []int32
}

const noSwell = 255

type isleFx struct {
	land  []landPx
	shore []shorePx
	lava  []shorePx
}

// landPx is one pixel of an island: its height, and the water it becomes once the island sinks.
type landPx struct {
	k   int32
	h   float32
	sea rgb
}

type shorePx struct {
	k  int32
	ph float32
}

// edge is how far out an island's shore reaches at the bearing cs, sn, relative to its radius.
func edge(seed uint64, cs, sn float64) float64 {
	return .78 + .46*scene.Fbm2(seed, cs*1.3+7, sn*1.3+7, 2)
}

// height is island is's ground at pixel x, y: 0 at the waterline, about 1 on top, below zero offshore.
func height(is *isle, x, y float64) float64 {
	dx, dy := x-is.cx, y-is.cy
	dist := math.Hypot(dx, dy)
	cs, sn := 1.0, 0.0
	if dist > 0 {
		cs, sn = dx/dist, dy/dist
	}
	h := 1 - dist/(is.r*edge(is.seed, cs, sn))
	if h > 0 {
		h += .22 * (scene.Fbm2(is.seed+5, x*.22, y*.22, 2) - .5) * smooth(0, .5, h)
	}
	return h
}

func (is *isle) reach() float64 { return is.r*1.8 + 4 }

func build(pal palette, l layout, w, rows int) *backdrop {
	p := rows * 2
	b := &backdrop{pal: pal, w: w, p: p, px: make([]rgb, w*p), phase: make([]uint8, w*p), field: make([]float32, w*p),
		own: make([]int16, w*p), fx: make([]isleFx, len(l.isles))}
	for k := range b.field {
		b.field[k], b.own[k] = -9, -1
	}
	for i := range l.isles {
		is := &l.isles[i]
		b.each(is.cx, is.cy, is.reach(), func(x, y, k int) {
			if h := float32(height(is, float64(x)+.5, float64(y)+.5)); h > b.field[k] {
				b.field[k], b.own[k] = h, int16(i)
			}
		})
	}
	var lagoon []float32
	for _, g := range l.groups {
		if g.la <= 0 {
			continue
		}
		if lagoon == nil {
			lagoon = make([]float32, w*p)
		}
		b.each(g.lx, g.ly, math.Max(g.la, g.lb)+2, func(x, y, k int) {
			fx, fy := float64(x)+.5, float64(y)+.5
			e := math.Hypot((fx-g.lx)/g.la, (fy-g.ly)/g.lb) + .14*(scene.Noise2(g.seed, fx*.12, fy*.12)-.5)
			if v := float32(smooth(1.04, .84, e)); v > lagoon[k] {
				lagoon[k] = v
			}
		})
	}
	for y := range p {
		for x := range w {
			k := y*w + x
			fx, fy := float64(x)+.5, float64(y)+.5
			sea := mix(pal.deep, pal.sea, .2+.65*scene.Fbm2(0x5ea5, fx*.025, fy*.045, 3))
			h := float64(b.field[k])
			if i := b.own[k]; i >= 0 && h < 0 {
				s := smooth(-.6, 0, h)
				sea = mix(sea, pal.shoal, s*s*.9)
				sea = mix(sea, pal.wet, smooth(-.05, 0, h)*.45)
			}
			if lagoon != nil && lagoon[k] > 0 {
				sea = mix(sea, pal.lagoon, float64(lagoon[k])*.6)
			}
			i := b.own[k]
			if i < 0 || h < 0 {
				b.px[k] = sea
				b.phase[k] = uint8(int(fx*.9+fy*1.7+18*scene.Noise2(0x77, fx*.08, fy*.08)) & 31)
				if i >= 0 && h >= -.16 {
					is := &l.isles[i]
					b.fx[i].shore = append(b.fx[i].shore, shorePx{int32(k), float32(math.Atan2(fy-is.cy, fx-is.cx)*5 + unit(is.seed, 9)*6)})
				}
				continue
			}
			is := &l.isles[i]
			b.phase[k] = noSwell
			nw := h
			if x > 0 && y > 0 {
				nw = float64(b.field[k-w-1])
			}
			b.px[k] = b.ground(is, fx, fy, h, h-nw)
			b.fx[i].land = append(b.fx[i].land, landPx{int32(k), float32(h), sea})
			if is.weather == scene.Eruption && b.molten(is, fx, fy, h) {
				b.fx[i].lava = append(b.fx[i].lava, shorePx{int32(k), float32(unit(is.seed, k) * 6.28)})
			}
		}
	}
	for i := range l.isles {
		b.tint(i, &l.isles[i])
	}
	for y := range p / 2 {
		for x := range w {
			up, down := (2*y)*w+x, (2*y+1)*w+x
			if b.field[up] < -.7 && b.field[down] < -.7 && (lagoon == nil || lagoon[up] < .2) {
				b.waves = append(b.waves, int32(y*w+x))
			}
		}
	}
	return b
}

// each calls f on every pixel within reach of x, y.
func (b *backdrop) each(cx, cy, reach float64, f func(x, y, k int)) {
	for y := max(0, int(cy-reach)); y < min(b.p, int(cy+reach)+1); y++ {
		for x := max(0, int(cx-reach)); x < min(b.w, int(cx+reach)+1); x++ {
			f(x, y, y*b.w+x)
		}
	}
}

// ground colors island land at height h: beach, then scrub, greening down from the top as the work gets done,
// hills above and bare rock on the peak; slope is the rise from the northwest, which the light catches.
func (b *backdrop) ground(is *isle, x, y, h, slope float64) rgb {
	pal := b.pal
	n := scene.Noise2(is.seed+9, x*.3, y*.3)
	c := mix(pal.wet, pal.sand, smooth(0, .06, h))
	if h >= .14 {
		c = mix(pal.sand, pal.grass, .25+.15*n)
		green := .14 + (1-is.progress)*.56
		c = mix(c, mix(pal.grass, pal.lush, n), smooth(green, green+.06, h))
		c = mix(c, pal.hill, smooth(.6, .95, h)*.8)
		c = mix(c, pal.rock, smooth(.92, 1.05, h))
	}
	if is.weather == scene.Eruption {
		c = mix(c, pal.ash, smooth(.4, .62, h))
		c = mix(c, pal.crater, smooth(.78, .86, h))
	}
	return c.Scale(1 + clamp(slope*5, -.3, .3))
}

// molten is whether a volcano's pixel runs with lava: its crater pool and the streams down its flanks.
func (b *backdrop) molten(is *isle, x, y, h float64) bool {
	if h > .86 {
		return true
	}
	ang := math.Atan2(y-is.cy, x-is.cx)
	return h > .2 && math.Abs(math.Sin(ang*2+unit(is.seed, 4)*6.28+h*.8)) < .14
}

// tint lays an island's standing weather on the backdrop: the shadow of a storm cell, a summit's golden shallows.
func (b *backdrop) tint(i int, is *isle) {
	switch is.weather {
	case scene.Storm:
		b.each(is.cx, is.cy, is.reach(), func(x, y, k int) {
			d := math.Hypot(float64(x)+.5-is.cx, float64(y)+.5-is.cy) / is.r
			b.px[k] = b.px[k].Scale(1 - .35*smooth(1.8, .9, d))
		})
	case scene.Summit:
		b.each(is.cx, is.cy, is.reach(), func(x, y, k int) {
			if h := b.field[k]; int(b.own[k]) == i && h < 0 && h > -.5 {
				b.px[k] = mix(b.px[k], b.pal.gold, .3*smooth(-.5, -.1, float64(h)))
			}
		})
	}
}
