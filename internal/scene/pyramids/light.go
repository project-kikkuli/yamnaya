package pyramids

import "time"

// palette is the light over the desert at one time of day.
type palette struct {
	sky                                []stop
	dune, sand, sandShade, plateau     rgb
	causeway                           rgb
	stone, shade, sketch, ramp, casing rgb
	gold                               rgb
	water, reed, palm, trunk, sail     rgb
	haze, storm, stormDark             rgb
	dust, dustShade, sun               rgb
	worker, red, green, label, rim     rgb
	glow                               rgb
	// stars is how bright the sky is with them, day how bright with the sun.
	stars, day float64
}

type stop struct {
	at float64
	c  rgb
}

var dusk = palette{
	sky:  []stop{{0, hex("#0a0f2e")}, {.38, hex("#23295c")}, {.66, hex("#6a4a74")}, {.84, hex("#c8786a")}, {.94, hex("#f0a060")}, {1, hex("#ffd08a")}},
	dune: hex("#7a5a62"), sand: hex("#b98a5e"), sandShade: hex("#7e5a48"), plateau: hex("#9a7258"),
	causeway: hex("#d8b890"),
	stone:    hex("#d9a877"), shade: hex("#7d5e5a"), sketch: hex("#e6c9a0"), ramp: hex("#6e4a36"), casing: hex("#f2e2c4"),
	gold:  hex("#ffd04a"),
	water: hex("#1c2850"), reed: hex("#4a5a34"), palm: hex("#23361f"), trunk: hex("#4a3426"), sail: hex("#f0e0c8"),
	haze: hex("#f0d4a8"), storm: hex("#c49058"), stormDark: hex("#5e4030"),
	dust: hex("#a88a70"), dustShade: hex("#5a4436"), sun: hex("#ffb060"),
	worker: hex("#fff0b8"), red: hex("#ff4733"), green: hex("#7ee08a"), label: hex("#ffe6c0"), rim: hex("#fff4d6"),
	glow:  hex("#ffb070"),
	stars: .4, day: .2,
}

var day = palette{
	sky:  []stop{{0, hex("#1d5fc0")}, {.45, hex("#4a8fe0")}, {.8, hex("#a8cdef")}, {.93, hex("#e4e8e0")}, {1, hex("#f6ead0")}},
	dune: hex("#d6b88a"), sand: hex("#ecc890"), sandShade: hex("#c49c62"), plateau: hex("#c8a070"),
	causeway: hex("#f6ead0"),
	stone:    hex("#f2d8a6"), shade: hex("#b38d62"), sketch: hex("#7a5a3a"), ramp: hex("#9a6a44"), casing: hex("#fffaee"),
	gold:  hex("#d89a00"),
	water: hex("#2f7ab8"), reed: hex("#4e8a3a"), palm: hex("#2f6e2a"), trunk: hex("#6e5034"), sail: hex("#ffffff"),
	haze: hex("#fff8e8"), storm: hex("#c89a5a"), stormDark: hex("#8a6034"),
	dust: hex("#c4a684"), dustShade: hex("#8a6a4a"), sun: hex("#fffbe8"),
	worker: hex("#14213d"), red: hex("#e0241a"), green: hex("#1f9d3a"), label: hex("#3a2410"), rim: hex("#1a1030"),
	glow: hex("#ffe3a3"),
	day:  1,
}

var night = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#02040c")}, {.5, hex("#070d22")}, {.8, hex("#141a38")}, {.93, hex("#262a48")}, {1, hex("#3a3a58")}}
	t.dune, t.sand, t.sandShade, t.plateau = hex("#2a2a40"), hex("#4a4a62"), hex("#2e2e44"), hex("#3e3e56")
	t.causeway = hex("#6a6a84")
	t.stone, t.shade, t.sketch, t.ramp, t.casing = hex("#b4b4cc"), hex("#4c4c66"), hex("#8a8aa8"), hex("#3a3040"), hex("#b8b8d0")
	t.gold = hex("#e8c060")
	t.water, t.reed, t.palm, t.trunk, t.sail = hex("#070d22"), hex("#1e2a24"), hex("#101a14"), hex("#241c1c"), hex("#a8a8c0")
	t.haze, t.storm, t.stormDark = hex("#6a6a80"), hex("#6a5a50"), hex("#2a2228")
	t.dust, t.dustShade = hex("#5a5260"), hex("#2a2430")
	t.glow = hex("#ffc27a")
	t.stars, t.day = .6, 0
})

var dawn = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#141c40")}, {.45, hex("#3c4c88")}, {.75, hex("#b88aa6")}, {.9, hex("#f2a88e")}, {1, hex("#ffd6a0")}}
	t.stone, t.shade = hex("#c8a088"), hex("#6e5868")
	t.stars, t.day = .15, .3
})

var morning = with(day, func(t *palette) {
	t.sky = []stop{{0, hex("#2b63c0")}, {.45, hex("#5c97df")}, {.8, hex("#a9cdef")}, {1, hex("#f4ecdc")}}
	t.sun = hex("#fff0c8")
})

var golden = with(day, func(t *palette) {
	t.sky = []stop{{0, hex("#2a5ab0")}, {.5, hex("#6e9ad8")}, {.8, hex("#e8c08a")}, {.93, hex("#ffb070")}, {1, hex("#ffd890")}}
	t.stone, t.shade, t.sand = hex("#f0c088"), hex("#a07050"), hex("#e8b070")
	t.sun, t.glow = hex("#ffc870"), hex("#ffc070")
	t.day = .7
})

// daylight is the light through a day by local hour, the mountains' timing; between two keys it blends.
var daylight = []struct {
	hour float64
	t    palette
}{{0, night}, {5, night}, {6.5, dawn}, {8, morning}, {12, day}, {16, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// paletteAt is the light at a local time; a light terminal only lifts the shadows.
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
		t.shade = mix(t.shade, t.stone, .2)
		t.sandShade = mix(t.sandShade, t.sand, .2)
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
	return []*rgb{&t.dune, &t.sand, &t.sandShade, &t.plateau, &t.causeway, &t.stone, &t.shade, &t.sketch, &t.ramp,
		&t.casing, &t.gold, &t.water, &t.reed, &t.palm, &t.trunk, &t.sail, &t.haze, &t.storm, &t.stormDark, &t.dust,
		&t.dustShade, &t.sun, &t.worker, &t.red, &t.green, &t.label, &t.rim, &t.glow}
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
