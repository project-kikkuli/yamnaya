package carrier

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type rgb = scene.RGB

func hex(s string) rgb { return scene.Hex(s) }

// light is the scene's colors at one time of day.
type light struct {
	zenith, horizon, seaFar, seaNear, foam rgb
	hull, deck, island, hullText, label    rgb
	crew, red, green, gold, plane          rgb
	fire, fireHot, smoke, cloud, rain      rgb
	window, sun                            rgb
	// stars is how bright the night is: stars, the moon and lit windows show above zero.
	stars float64
}

var night = light{
	zenith: hex("#02040c"), horizon: hex("#16244a"), seaFar: hex("#0d1b38"), seaNear: hex("#03070f"), foam: hex("#5c74a0"),
	hull: hex("#2b3343"), deck: hex("#111318"), island: hex("#3a4458"), hullText: hex("#c7d0e2"), label: hex("#e6ebf5"),
	crew: hex("#ffd84a"), red: hex("#ff4436"), green: hex("#5ee08a"), gold: hex("#ffcf5a"), plane: hex("#c4cede"),
	fire: hex("#ff8a1e"), fireHot: hex("#ffe27a"), smoke: hex("#2a2a33"), cloud: hex("#46506c"), rain: hex("#7d93bd"),
	window: hex("#ffd98a"), sun: hex("#e8ecff"), stars: 1,
}

var dawn = with(night, func(l *light) {
	l.zenith, l.horizon, l.seaFar, l.seaNear, l.foam = hex("#1a2350"), hex("#f2a98f"), hex("#8a7aa0"), hex("#1f2a4a"), hex("#ffd8c8")
	l.hull, l.island, l.hullText, l.label = hex("#4a4f62"), hex("#5a6074"), hex("#f0e6e0"), hex("#fff0e6")
	l.cloud, l.rain, l.sun, l.stars = hex("#e8b8c0"), hex("#c0b8d8"), hex("#ffc070"), .25
})

var day = with(night, func(l *light) {
	l.zenith, l.horizon, l.seaFar, l.seaNear, l.foam = hex("#2a66c8"), hex("#cfe3f6"), hex("#3f86bf"), hex("#0f3f70"), hex("#ffffff")
	l.hull, l.island, l.hullText, l.label = hex("#5d6673"), hex("#77808e"), hex("#ffffff"), hex("#ffffff")
	l.red, l.green, l.gold, l.plane = hex("#e8231a"), hex("#39d06a"), hex("#ffc93a"), hex("#e3e8ef")
	l.fire, l.smoke, l.cloud, l.rain, l.sun, l.stars = hex("#ff7a12"), hex("#4b4b52"), hex("#f2f5f9"), hex("#d0dae6"), hex("#fff6c8"), 0
})

var golden = with(day, func(l *light) {
	l.zenith, l.horizon, l.seaFar, l.seaNear, l.foam = hex("#2a5ab0"), hex("#ffc68a"), hex("#6a86a8"), hex("#173c66"), hex("#fff0d0")
	l.hull, l.island, l.hullText, l.cloud, l.sun = hex("#6a6266"), hex("#807670"), hex("#fff3e0"), hex("#ffe0b8"), hex("#ffb050")
})

var dusk = with(night, func(l *light) {
	l.zenith, l.horizon, l.seaFar, l.seaNear, l.foam = hex("#070c22"), hex("#e5935f"), hex("#3a3d66"), hex("#071024"), hex("#f0b090")
	l.hull, l.island, l.hullText, l.cloud, l.sun, l.stars = hex("#353548"), hex("#474a60"), hex("#f0d8c8"), hex("#a88aa0"), hex("#ff9a50"), .6
})

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

// lightAt is the light at a local time; a light terminal lifts the night sea and sky so the board stays easy to find.
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
		l.zenith = scene.Mix(l.zenith, l.horizon, .25)
		l.seaNear = scene.Mix(l.seaNear, l.seaFar, .35)
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
	out.stars = scene.Lerp(a.stars, b.stars, f)
	return out
}

func (l *light) colors() []*rgb {
	return []*rgb{&l.zenith, &l.horizon, &l.seaFar, &l.seaNear, &l.foam, &l.hull, &l.deck, &l.island, &l.hullText, &l.label,
		&l.crew, &l.red, &l.green, &l.gold, &l.plane, &l.fire, &l.fireHot, &l.smoke, &l.cloud, &l.rain, &l.window, &l.sun}
}
