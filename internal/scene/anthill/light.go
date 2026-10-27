package anthill

import "time"

// palette is the light over the colony at one time of day; underground it changes only a little.
type palette struct {
	sky                          []stop
	sun                          rgb
	grass, mound, moundLit, wall rgb
	label, rim                   rgb
	dust, dustShade              rgb
	storm, stormTop, rain        rgb
	glow, alate                  rgb
	humus, deep, pebble, root    rgb
	soil                         [4]rgb
	cavity, comb, rubble         rgb
	water, waterLit              rgb
	gold, green, open            rgb
	ant, red, rest, ink, inkRim  rgb
	anteater, stripe, tongue     rgb
	// stars is how bright the night sky is; sun is 1 for the sun's disc and 0 for the moon's.
	stars, day float64
}

type stop struct {
	at float64
	c  rgb
}

var dusk = palette{
	sky:   []stop{{0, hex("#0b1030")}, {.45, hex("#2a2c5e")}, {.75, hex("#8a5a78")}, {.92, hex("#e8916a")}, {1, hex("#ffc88a")}},
	sun:   hex("#ffb070"),
	grass: hex("#3e6a2e"), mound: hex("#8a6040"), moundLit: hex("#b07a4c"), wall: hex("#4a301e"),
	label: hex("#ffe8c8"), rim: hex("#fff2d0"),
	dust: hex("#b09474"), dustShade: hex("#5e4c3a"),
	storm: hex("#1e2232"), stormTop: hex("#565e78"), rain: hex("#9aaacc"),
	glow: hex("#ffc07a"), alate: hex("#ffe6a8"),
	humus: hex("#2c1c12"), deep: hex("#24160e"), pebble: hex("#8a8078"), root: hex("#6e4a2a"),
	soil:   [4]rgb{hex("#4c3220"), hex("#3f2919"), hex("#48301f"), hex("#362315")},
	cavity: hex("#120a06"), comb: hex("#22150c"), rubble: hex("#6a4e34"),
	water: hex("#2a68b4"), waterLit: hex("#78b4ee"),
	gold: hex("#f0b840"), green: hex("#7ee07e"), open: hex("#d8c0a0"),
	ant: hex("#ffc857"), red: hex("#ff3b30"), rest: hex("#a8988a"), ink: hex("#f0dcc0"), inkRim: hex("#ffe08a"),
	anteater: hex("#3c352e"), stripe: hex("#dcd2c2"), tongue: hex("#e88aa0"),
	stars: .35, day: .3,
}

var night = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#02040c")}, {.5, hex("#08102a")}, {.85, hex("#16203e")}, {1, hex("#2a2c48")}}
	t.sun = hex("#e8ecf8")
	t.grass, t.mound, t.moundLit, t.wall = hex("#1c3424"), hex("#4e3a2c"), hex("#625040"), hex("#2a1c14")
	t.dust, t.dustShade = hex("#8a7860"), hex("#4a3e30")
	t.glow = hex("#e0b070")
	t.soil = [4]rgb{hex("#3e291a"), hex("#332114"), hex("#3a2718"), hex("#2c1c10")}
	t.stars, t.day = .7, 0
})

var dawn = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#18204a")}, {.5, hex("#4a5490")}, {.8, hex("#c09ab0")}, {.93, hex("#f6b090")}, {1, hex("#ffdcaa")}}
	t.sun = hex("#ffc890")
	t.stars, t.day = .12, .4
})

var day = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#2a68c8")}, {.5, hex("#5c9ae0")}, {.85, hex("#a8d0f2")}, {1, hex("#dcecf8")}}
	t.sun = hex("#fff6d0")
	t.grass, t.mound, t.moundLit, t.wall = hex("#5a9a38"), hex("#b8844e"), hex("#d6a46a"), hex("#6e4a2c")
	t.label, t.rim = hex("#2a1a0c"), hex("#120a04")
	t.dust, t.dustShade = hex("#d2bc98"), hex("#9a8262")
	t.storm, t.stormTop, t.rain = hex("#4a5262"), hex("#8e98aa"), hex("#33507a")
	t.glow, t.alate = hex("#fff0b0"), hex("#4a3010")
	t.stars, t.day = 0, 1
})

var golden = with(day, func(t *palette) {
	t.sky = []stop{{0, hex("#2c5cb0")}, {.5, hex("#7a9cd4")}, {.82, hex("#eac08a")}, {1, hex("#ffd890")}}
	t.sun = hex("#ffd27a")
	t.mound, t.moundLit = hex("#b47a46"), hex("#e0a060")
	t.day = .8
})

// daylight is the light through a day by local hour, the mountains' timing; between two keys it blends.
var daylight = []struct {
	hour float64
	t    palette
}{{0, night}, {5, night}, {6.5, dawn}, {8, day}, {16, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// hourOf is the local hour as a fraction; the zero time is dusk.
func hourOf(now time.Time) float64 {
	if now.IsZero() {
		return 19.5
	}
	return float64(now.Hour()) + float64(now.Minute())/60 + float64(now.Second())/3600
}

// paletteAt is the light at a local time; a light terminal only lifts the soil's deepest shadows.
func paletteAt(now time.Time, dark bool) palette {
	hour := hourOf(now)
	t := daylight[len(daylight)-1].t
	for i := 1; i < len(daylight); i++ {
		if a, b := daylight[i-1], daylight[i]; hour <= b.hour {
			t = blend(a.t, b.t, smooth(0, 1, (hour-a.hour)/(b.hour-a.hour)))
			break
		}
	}
	if !dark {
		t.deep = mix(t.deep, t.soil[0], .35)
		t.cavity = mix(t.cavity, t.comb, .4)
	}
	return t
}

func with(base palette, change func(*palette)) palette {
	base.sky = append([]stop(nil), base.sky...)
	change(&base)
	return base
}

func blend(a, b palette, f float64) palette {
	out := a
	ca, cb, co := a.colors(), b.colors(), out.colors()
	for i := range co {
		*co[i] = mix(*ca[i], *cb[i], f)
	}
	out.stars, out.day = lerp(a.stars, b.stars, f), lerp(a.day, b.day, f)
	out.sky = nil
	for k := range 13 {
		v := float64(k) / 12
		out.sky = append(out.sky, stop{v, mix(a.skyAt(v), b.skyAt(v), f)})
	}
	return out
}

func (t *palette) colors() []*rgb {
	return []*rgb{&t.sun, &t.grass, &t.mound, &t.moundLit, &t.wall, &t.label, &t.rim, &t.dust, &t.dustShade, &t.storm,
		&t.stormTop, &t.rain, &t.glow, &t.alate, &t.humus, &t.deep, &t.pebble, &t.root, &t.soil[0], &t.soil[1],
		&t.soil[2], &t.soil[3], &t.cavity, &t.comb, &t.rubble, &t.water, &t.waterLit, &t.gold, &t.green, &t.open,
		&t.ant, &t.red, &t.rest, &t.ink, &t.inkRim, &t.anteater, &t.stripe, &t.tongue}
}

func (t palette) skyAt(v float64) rgb {
	v = clamp(v, 0, 1)
	for i := 1; i < len(t.sky); i++ {
		if v <= t.sky[i].at {
			a, b := t.sky[i-1], t.sky[i]
			return mix(a.c, b.c, smooth(0, 1, (v-a.at)/(b.at-a.at)))
		}
	}
	return t.sky[len(t.sky)-1].c
}
