package kaiju

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type rgb = scene.RGB

func hex(s string) rgb                 { return scene.Hex(s) }
func mix(a, b rgb, t float64) rgb      { return scene.Mix(a, b, t) }
func clamp(v, lo, hi float64) float64  { return scene.Clamp(v, lo, hi) }
func smooth(lo, hi, v float64) float64 { return scene.Smooth(lo, hi, v) }
func unit(seed uint64, i int) float64  { return scene.Unit(seed, i) }
func lerp(a, b, t float64) float64     { return scene.Lerp(a, b, t) }

// palette is the film stock at one time of day: how its greys print, the greys of the set, and the few colors
// that show through it.
type palette struct {
	shadow, mid, high rgb

	zenith, horizon, city, far, window, quay, shore, deep float64
	beam, body, rim, smoke, moon                          float64

	jet, flare, green, gold, label, dim, breath, fire, fireHot rgb
}

var night = palette{
	shadow: hex("#050608"), mid: hex("#4c5260"), high: hex("#e4e7ee"),
	zenith: .08, horizon: .36, city: .07, far: .2, window: .72, quay: .12, shore: .3, deep: .09,
	beam: .3, body: .03, rim: .55, smoke: .28, moon: .95,
	jet: hex("#fffbe8"), flare: hex("#ff3a2e"), green: hex("#9ad6a6"), gold: hex("#ead08a"),
	label: hex("#f2ead8"), dim: hex("#a8a294"), breath: hex("#c4ecff"), fire: hex("#ffaa38"), fireHot: hex("#ffe9a8"),
}

var dusk = palette{
	shadow: hex("#0e0906"), mid: hex("#6e5a45"), high: hex("#f2dfbf"),
	zenith: .14, horizon: .5, city: .08, far: .26, window: .7, quay: .14, shore: .36, deep: .1,
	beam: .2, body: .05, rim: .58, smoke: .34, moon: .9,
	jet: hex("#fffbe8"), flare: hex("#ff3a2e"), green: hex("#9ad6a6"), gold: hex("#ead08a"),
	label: hex("#f6ead2"), dim: hex("#b4a68e"), breath: hex("#c4ecff"), fire: hex("#ffaa38"), fireHot: hex("#ffe9a8"),
}

var day = palette{
	shadow: hex("#0c0c0b"), mid: hex("#76746f"), high: hex("#f3f1eb"),
	zenith: .58, horizon: .84, city: .24, far: .55, window: .42, quay: .32, shore: .62, deep: .3,
	beam: .06, body: .12, rim: .52, smoke: .5, moon: .97,
	jet: hex("#ffffff"), flare: hex("#f0281e"), green: hex("#5fbf74"), gold: hex("#d8b04e"),
	label: hex("#ffffff"), dim: hex("#e8e4da"), breath: hex("#a8e0ff"), fire: hex("#ff9e1f"), fireHot: hex("#ffe28a"),
}

var dawn = with(dusk, func(p *palette) {
	p.mid, p.high = hex("#6c6158"), hex("#f0e2cc")
	p.zenith, p.horizon = .16, .5
})

var golden = with(day, func(p *palette) {
	p.mid, p.high = hex("#7a6a56"), hex("#f6e6c8")
	p.zenith, p.horizon, p.far = .5, .78, .5
})

// daylight is the stock through a day, by local hour; between two keys it blends.
var daylight = []struct {
	hour float64
	p    palette
}{{0, night}, {5, night}, {6.5, dawn}, {8, day}, {16.5, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// paletteAt is the stock at a local time; a light terminal lifts the shadows so the board stays easy to find.
func paletteAt(now time.Time, dark bool) palette {
	hour := 19.5
	if !now.IsZero() {
		hour = float64(now.Hour()) + float64(now.Minute())/60 + float64(now.Second())/3600
	}
	p := daylight[len(daylight)-1].p
	for i := 1; i < len(daylight); i++ {
		if a, b := daylight[i-1], daylight[i]; hour <= b.hour {
			p = blend(a.p, b.p, smooth(0, 1, (hour-a.hour)/(b.hour-a.hour)))
			break
		}
	}
	if !dark {
		p.shadow = mix(p.shadow, p.mid, .25)
	}
	return p
}

func with(base palette, change func(*palette)) palette {
	change(&base)
	return base
}

func blend(a, b palette, f float64) palette {
	out := a
	ca, cb, co := a.colors(), b.colors(), out.colors()
	for i := range co {
		*co[i] = mix(*ca[i], *cb[i], f)
	}
	ga, gb, gs := a.greys(), b.greys(), out.greys()
	for i := range gs {
		*gs[i] = lerp(*ga[i], *gb[i], f)
	}
	return out
}

func (p *palette) colors() []*rgb {
	return []*rgb{&p.shadow, &p.mid, &p.high, &p.jet, &p.flare, &p.green, &p.gold, &p.label, &p.dim, &p.breath, &p.fire, &p.fireHot}
}

func (p *palette) greys() []*float64 {
	return []*float64{&p.zenith, &p.horizon, &p.city, &p.far, &p.window, &p.quay, &p.shore, &p.deep, &p.beam, &p.body,
		&p.rim, &p.smoke, &p.moon}
}

// print is how the stock prints each of 256 greys: shadows through the midtone to the highlights.
func (p *palette) print() *[256]rgb {
	var out [256]rgb
	for i := range out {
		v := float64(i) / 255
		if v < .5 {
			out[i] = mix(p.shadow, p.mid, v*2)
		} else {
			out[i] = mix(p.mid, p.high, (v-.5)*2)
		}
	}
	return &out
}
