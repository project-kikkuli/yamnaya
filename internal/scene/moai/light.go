package moai

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type rgb = scene.RGB

func hex(s string) rgb { return scene.Hex(s) }

// light is the island's colors at one time of day.
type light struct {
	zenith, horizon, hillFar, hillNear, grass, seaFar, seaNear, foam rgb
	stone, stoneLit, stoneShade, ahu, ahuText, label, coral          rgb
	carver, resting, red, green, scoria, rock                        rgb
	mist, rain, dust, sun, star                                      rgb
	// stars is how bright the night is: stars and the Milky Way show above zero.
	stars float64
}

var night = light{
	zenith: hex("#02030b"), horizon: hex("#141d3e"), hillFar: hex("#0f1d24"), hillNear: hex("#0a1712"), grass: hex("#16301e"),
	seaFar: hex("#0b1834"), seaNear: hex("#030812"), foam: hex("#5d77a6"),
	stone: hex("#5d5a5c"), stoneLit: hex("#837d78"), stoneShade: hex("#26252b"), ahu: hex("#3b3a40"), ahuText: hex("#d6d2cc"),
	label: hex("#e8eaf2"), coral: hex("#f4f4ff"),
	carver: hex("#ffe27a"), resting: hex("#8f9aa8"), red: hex("#ff4436"), green: hex("#5ee08a"), scoria: hex("#b8673a"), rock: hex("#a7a4a2"),
	mist: hex("#4a5577"), rain: hex("#7d93bd"), dust: hex("#6d6458"), sun: hex("#e8ecff"), star: hex("#dfe6ff"), stars: 1,
}

var dawn = with(night, func(l *light) {
	l.zenith, l.horizon, l.hillFar, l.hillNear, l.grass = hex("#1a2350"), hex("#f2a98f"), hex("#5b5a72"), hex("#2f3d34"), hex("#3f5a3a")
	l.seaFar, l.seaNear, l.foam = hex("#8a7aa0"), hex("#1f2a4a"), hex("#ffd8c8")
	l.stone, l.stoneLit, l.stoneShade, l.ahu = hex("#7a6a6a"), hex("#b0948a"), hex("#3a3040"), hex("#5a5058")
	l.mist, l.rain, l.dust, l.sun, l.stars = hex("#e8c0c8"), hex("#c0b8d8"), hex("#9a8070"), hex("#ffc070"), .25
})

var day = with(night, func(l *light) {
	l.zenith, l.horizon, l.hillFar, l.hillNear, l.grass = hex("#2a66c8"), hex("#cfe3f6"), hex("#6f9a6a"), hex("#4f8a3e"), hex("#5f9e44")
	l.seaFar, l.seaNear, l.foam = hex("#3f86bf"), hex("#0f3f70"), hex("#ffffff")
	l.stone, l.stoneLit, l.stoneShade, l.ahu, l.ahuText = hex("#7d746a"), hex("#a89c8c"), hex("#3c3833"), hex("#615b55"), hex("#ffffff")
	l.carver, l.resting, l.red, l.green = hex("#ffd84a"), hex("#c8cdd4"), hex("#e8231a"), hex("#39d06a")
	l.scoria, l.rock = hex("#b06030"), hex("#d8d4cf")
	l.label = hex("#18202e")
	l.mist, l.rain, l.dust, l.sun, l.stars = hex("#f2f5f9"), hex("#d0dae6"), hex("#b8a890"), hex("#fff6c8"), 0
})

var golden = with(day, func(l *light) {
	l.zenith, l.horizon, l.hillFar, l.hillNear, l.grass = hex("#2a5ab0"), hex("#ffc68a"), hex("#8a8a5a"), hex("#5a7a36"), hex("#6f8c3a")
	l.seaFar, l.seaNear, l.foam = hex("#6a86a8"), hex("#173c66"), hex("#fff0d0")
	l.stone, l.stoneLit, l.ahu, l.mist, l.sun = hex("#8a7462"), hex("#d0a47a"), hex("#6a5a50"), hex("#ffe0b8"), hex("#ffb050")
})

var dusk = with(night, func(l *light) {
	l.zenith, l.horizon, l.hillFar, l.hillNear, l.grass = hex("#070c22"), hex("#e5935f"), hex("#2e2f48"), hex("#1a2620"), hex("#253a28")
	l.seaFar, l.seaNear, l.foam = hex("#3a3d66"), hex("#071024"), hex("#f0b090")
	l.stone, l.stoneLit, l.ahu = hex("#5f5560"), hex("#a07a6a"), hex("#403a48")
	l.mist, l.sun, l.stars = hex("#a88aa0"), hex("#ff9a50"), .6
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

// lightAt is the light at a local time; a light terminal lifts the night sky and hills so the board stays easy to find.
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
		l.hillNear = scene.Mix(l.hillNear, l.grass, .35)
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
	return []*rgb{&l.zenith, &l.horizon, &l.hillFar, &l.hillNear, &l.grass, &l.seaFar, &l.seaNear, &l.foam,
		&l.stone, &l.stoneLit, &l.stoneShade, &l.ahu, &l.ahuText, &l.label, &l.coral,
		&l.carver, &l.resting, &l.red, &l.green, &l.scoria, &l.rock, &l.mist, &l.rain, &l.dust, &l.sun, &l.star}
}
