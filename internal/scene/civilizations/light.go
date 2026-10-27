package civilizations

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type rgb = scene.RGB

func hex(s string) rgb { return scene.Hex(s) }

// light is the map's colors at one time of day.
type light struct {
	deep, ocean, coast, wave             rgb
	land, plain, forest, peak, road      rgb
	label, border, city, lit             rgb
	busy, rester, red, green, gold, fire rgb
	ash, ruin, fog, pall, steel          rgb
	tints                                [len(tintHexDay)]rgb
}

// tintHexDay are the civilizations' territory tints by day: blues, violets and teals, never red, green or gold.
var tintHexDay = [...]string{"#3d6fd1", "#8a52c4", "#1f9a9a", "#b04aa8", "#5a5fd8"}

var night = light{
	deep: hex("#06101f"), ocean: hex("#0c1d33"), coast: hex("#1d4460"), wave: hex("#4f7aa0"),
	land: hex("#2a3a24"), plain: hex("#3a3a28"), forest: hex("#5f8a4c"), peak: hex("#8f8676"), road: hex("#8a7450"),
	label: hex("#ece2c6"), border: hex("#cdbf96"), city: hex("#e8dcc0"), lit: hex("#ffd98a"),
	busy: hex("#ffe066"), rester: hex("#9a9a92"), red: hex("#ff3b30"), green: hex("#5ee08a"), gold: hex("#ffcf3a"), fire: hex("#ff8a1e"),
	ash: hex("#4a4540"), ruin: hex("#b0a898"), fog: hex("#8a909a"), pall: hex("#2a2438"), steel: hex("#d4dae4"),
	tints: tints(.55),
}

var dawn = with(night, func(l *light) {
	l.deep, l.ocean, l.coast, l.wave = hex("#1c2a52"), hex("#2f4572"), hex("#5c7ea0"), hex("#c8b8d8")
	l.land, l.plain, l.forest = hex("#5c6a48"), hex("#7a6e58"), hex("#3a5230")
	l.tints = tints(.8)
})

var day = with(night, func(l *light) {
	l.deep, l.ocean, l.coast, l.wave = hex("#2a5d8f"), hex("#3f7fb5"), hex("#7ec3d8"), hex("#d8f0ff")
	l.land, l.plain, l.forest, l.peak, l.road = hex("#9db86a"), hex("#c9bd84"), hex("#2f5e2a"), hex("#6e5f50"), hex("#7a5a30")
	l.label, l.border, l.city, l.lit = hex("#1c1a14"), hex("#3a2e1c"), hex("#fbf6e6"), hex("#fbf6e6")
	l.busy, l.rester, l.red, l.green, l.gold = hex("#14246a"), hex("#4a4a46"), hex("#e8231a"), hex("#1fa64a"), hex("#ffc21a")
	l.ash, l.ruin, l.fog, l.pall, l.steel = hex("#7a7068"), hex("#4a4038"), hex("#d8dce2"), hex("#4a4060"), hex("#1e2430")
	l.tints = tints(1)
})

var golden = with(day, func(l *light) {
	l.deep, l.ocean, l.coast, l.wave = hex("#2a4f86"), hex("#4a78a8"), hex("#a8c0c8"), hex("#ffe8c8")
	l.land, l.plain = hex("#a8a862"), hex("#d0b078")
	l.tints = tints(.9)
})

var dusk = with(night, func(l *light) {
	l.deep, l.ocean, l.coast, l.wave = hex("#0e1430"), hex("#1e2a52"), hex("#3e5272"), hex("#c09098")
	l.land, l.plain = hex("#3a4230"), hex("#4a4232")
	l.tints = tints(.7)
})

func tints(k float64) (out [len(tintHexDay)]rgb) {
	for i, h := range tintHexDay {
		out[i] = hex(h).Scale(k)
	}
	return out
}

// daylight is the light through a day by local hour; between two keys it blends.
var daylight = []struct {
	hour float64
	l    light
}{{0, night}, {5, night}, {6.5, dawn}, {8, day}, {16, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// hourOf is the local hour; a zero time is dusk.
func hourOf(now time.Time) float64 {
	if now.IsZero() {
		return 19.5
	}
	return float64(now.Hour()) + float64(now.Minute())/60 + float64(now.Second())/3600
}

// lightAt is the light at a local time; a light terminal lifts the night sea so the map stays easy to find.
func lightAt(now time.Time, dark bool) light {
	hour := hourOf(now)
	l := daylight[len(daylight)-1].l
	for i := 1; i < len(daylight); i++ {
		if a, b := daylight[i-1], daylight[i]; hour <= b.hour {
			l = blend(a.l, b.l, scene.Smooth(0, 1, (hour-a.hour)/(b.hour-a.hour)))
			break
		}
	}
	if !dark {
		l.deep = scene.Mix(l.deep, l.coast, .3)
		l.ocean = scene.Mix(l.ocean, l.coast, .3)
	}
	return l
}

func with(base light, change func(*light)) light {
	change(&base)
	return base
}

func blend(a, b light, f float64) light {
	out := a
	ca, cb, co := a.colors(), b.colors(), out.colors()
	for i := range co {
		*co[i] = scene.Mix(*ca[i], *cb[i], f)
	}
	return out
}

func (l *light) colors() []*rgb {
	out := []*rgb{&l.deep, &l.ocean, &l.coast, &l.wave, &l.land, &l.plain, &l.forest, &l.peak, &l.road,
		&l.label, &l.border, &l.city, &l.lit, &l.busy, &l.rester, &l.red, &l.green, &l.gold, &l.fire,
		&l.ash, &l.ruin, &l.fog, &l.pall, &l.steel}
	for i := range l.tints {
		out = append(out, &l.tints[i])
	}
	return out
}
