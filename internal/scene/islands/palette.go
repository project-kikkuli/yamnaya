package islands

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

// palette is the light over the sea at one time of day.
type palette struct {
	deep, sea, shoal, lagoon, foam rgb
	sand, wet, grass, lush, hill   rgb
	rock, ash, crater              rgb
	lava, lavaHot, smoke           rgb
	cloud, storm, bolt             rgb
	boat, flare, green, gold       rgb
	label, rim, dim, glint, open   rgb
}

var dusk = palette{
	deep: hex("#0a1838"), sea: hex("#1b3062"), shoal: hex("#3d6390"), lagoon: hex("#4f8fa0"), foam: hex("#ffe4d2"),
	sand: hex("#caa48a"), wet: hex("#8e7468"), grass: hex("#4a6a46"), lush: hex("#2a4632"), hill: hex("#62604a"),
	rock: hex("#6e5862"), ash: hex("#2e2629"), crater: hex("#1a1214"),
	lava: hex("#ff6418"), lavaHot: hex("#fff3a6"), smoke: hex("#8a8088"),
	cloud: hex("#d6a9b5"), storm: hex("#151a28"), bolt: hex("#f2ecff"),
	boat: hex("#fff0b8"), flare: hex("#ff4733"), green: hex("#7ee08a"), gold: hex("#ffd36b"),
	label: hex("#ffe6c0"), rim: hex("#ffe0b0"), dim: hex("#b8b0d0"), glint: hex("#ffb486"), open: hex("#e6dcd4"),
}

var day = palette{
	deep: hex("#0b3f78"), sea: hex("#1866a8"), shoal: hex("#3aaecb"), lagoon: hex("#6fdcd4"), foam: hex("#f2fbff"),
	sand: hex("#efdca2"), wet: hex("#c9b47e"), grass: hex("#57a843"), lush: hex("#2f7d33"), hill: hex("#7b8a4a"),
	rock: hex("#8d8476"), ash: hex("#4a4440"), crater: hex("#2a1e1c"),
	lava: hex("#ff5a14"), lavaHot: hex("#fff0a0"), smoke: hex("#a4a2a8"),
	cloud: hex("#ffffff"), storm: hex("#2f3644"), bolt: hex("#ffffff"),
	boat: hex("#ffffff"), flare: hex("#e0241a"), green: hex("#1f9d3a"), gold: hex("#e0a400"),
	label: hex("#ffffff"), rim: hex("#ffffff"), dim: hex("#d4e6f4"), glint: hex("#ffffff"), open: hex("#ece8de"),
}

var night = palette{
	deep: hex("#020814"), sea: hex("#051a33"), shoal: hex("#0e4060"), lagoon: hex("#17606c"), foam: hex("#9cc0dc"),
	sand: hex("#6e6650"), wet: hex("#4c4636"), grass: hex("#24482e"), lush: hex("#12301e"), hill: hex("#36422e"),
	rock: hex("#3a3640"), ash: hex("#201c20"), crater: hex("#120c0e"),
	lava: hex("#ff6418"), lavaHot: hex("#fff3a6"), smoke: hex("#4c464a"),
	cloud: hex("#5d6990"), storm: hex("#0c1018"), bolt: hex("#e8ecff"),
	boat: hex("#fff0b8"), flare: hex("#ff4733"), green: hex("#7ee08a"), gold: hex("#ffd36b"),
	label: hex("#ffe6c0"), rim: hex("#dfe8ff"), dim: hex("#90a4c8"), glint: hex("#c9d6ff"), open: hex("#c8c8d4"),
}

var dawn = with(dusk, func(p *palette) {
	p.deep, p.sea, p.shoal, p.lagoon = hex("#142450"), hex("#2a4078"), hex("#5a7aa8"), hex("#7aaab8")
	p.sand, p.grass, p.cloud = hex("#e8b8a0"), hex("#4e7452"), hex("#f2b8c0")
	p.glint, p.rim = hex("#ffc2a8"), hex("#fff0e0")
})

var golden = with(day, func(p *palette) {
	p.deep, p.sea, p.shoal = hex("#123e70"), hex("#2a6aa0"), hex("#5aaabc")
	p.sand, p.grass, p.cloud = hex("#f0c890"), hex("#6a9a40"), hex("#ffe0b0")
	p.glint, p.rim = hex("#ffc070"), hex("#fff0c0")
})

// daylight is the light through a day, by local hour; between two keys it blends.
var daylight = []struct {
	hour float64
	p    palette
}{{0, night}, {5, night}, {6.5, dawn}, {8, day}, {16.5, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// paletteAt is the light at a local time; a light terminal only lifts the deep water so the board stays easy to find.
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
		p.deep = mix(p.deep, p.sea, .3)
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
	return out
}

func (p *palette) colors() []*rgb {
	return []*rgb{&p.deep, &p.sea, &p.shoal, &p.lagoon, &p.foam, &p.sand, &p.wet, &p.grass, &p.lush, &p.hill,
		&p.rock, &p.ash, &p.crater, &p.lava, &p.lavaHot, &p.smoke, &p.cloud, &p.storm, &p.bolt,
		&p.boat, &p.flare, &p.green, &p.gold, &p.label, &p.rim, &p.dim, &p.glint, &p.open}
}
