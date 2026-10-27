package skyline

import "time"

// palette is the light of the city at one time of day.
type palette struct {
	sky                                  []stop
	far, farLit                          rgb
	facade, slab, glass, lit             rgb
	steel, crane                         rgb
	sidewalk, road, lane, water, lamp    rgb
	podium, tail                         rgb
	smog, smogShade                      rgb
	stormSky, stormCloud, stormTop, rain rgb
	bolt                                 rgb
	fire, fireHot, smoke                 rgb
	kaiju, kaijuGlow, eye                rgb
	dust, dustShade                      rgb
	worker, red, green, gold             rgb
	label, rim, glow                     rgb
	// lights is how lit the city is: windows, lamps and headlights; stars how bright the sky over it.
	lights, stars float64
}

type stop struct {
	at float64
	c  rgb
}

var dusk = palette{
	sky: []stop{{0, hex("#050a1a")}, {.38, hex("#10204a")}, {.66, hex("#3b3f73")}, {.84, hex("#b4707a")}, {.94, hex("#ec9e64")}, {1, hex("#ffd08a")}},
	far: hex("#2c2848"), farLit: hex("#e8b070"),
	facade: hex("#3a3550"), slab: hex("#25223a"), glass: hex("#4a4a78"), lit: hex("#ffd27a"),
	steel: hex("#9a8a80"), crane: hex("#f2b134"),
	sidewalk: hex("#4a4458"), road: hex("#1e1c28"), lane: hex("#c8b070"), water: hex("#0d1a33"), lamp: hex("#ffd890"),
	podium: hex("#4e4860"), tail: hex("#ffaa40"),
	smog: hex("#9a8466"), smogShade: hex("#4a4038"),
	stormSky: hex("#1a1f2d"), stormCloud: hex("#23293a"), stormTop: hex("#5c6480"), rain: hex("#8a94b0"),
	bolt: hex("#f2ecff"),
	fire: hex("#ff8a2a"), fireHot: hex("#ffe08a"), smoke: hex("#2e2629"),
	kaiju: hex("#141c1a"), kaijuGlow: hex("#5ff0ff"), eye: hex("#ffd040"),
	dust: hex("#c8b89a"), dustShade: hex("#6a5e50"),
	worker: hex("#fff0b8"), red: hex("#ff4733"), green: hex("#7ee08a"), gold: hex("#ffd36b"),
	label: hex("#ffe6c0"), rim: hex("#ffe0b0"), glow: hex("#ffad7e"),
	lights: .75, stars: .4,
}

var day = palette{
	sky: []stop{{0, hex("#1a56b8")}, {.4, hex("#3a83db")}, {.75, hex("#82b8ec")}, {.92, hex("#bcd9f3")}, {1, hex("#e6f0f8")}},
	far: hex("#93a6c4"), farLit: hex("#c8d8ec"),
	facade: hex("#9aa1ad"), slab: hex("#6c7382"), glass: hex("#b8d4ee"), lit: hex("#fff4cc"),
	steel: hex("#5e626c"), crane: hex("#e8a020"),
	sidewalk: hex("#b0aca4"), road: hex("#4a4c52"), lane: hex("#f0f0e8"), water: hex("#2f6ea8"), lamp: hex("#e8e0c8"),
	podium: hex("#a29e96"), tail: hex("#e08a20"),
	smog: hex("#b0a07e"), smogShade: hex("#7a6e58"),
	stormSky: hex("#556070"), stormCloud: hex("#3f4755"), stormTop: hex("#8a94a3"), rain: hex("#c0cad8"),
	bolt: hex("#ffffff"),
	fire: hex("#ff7a1a"), fireHot: hex("#ffd060"), smoke: hex("#55525a"),
	kaiju: hex("#22302a"), kaijuGlow: hex("#22c8e8"), eye: hex("#ffc020"),
	dust: hex("#d2c4aa"), dustShade: hex("#8a7c68"),
	worker: hex("#14213d"), red: hex("#e0241a"), green: hex("#1f9d3a"), gold: hex("#c98a00"),
	label: hex("#ffffff"), rim: hex("#ffffff"), glow: hex("#ffe3a3"),
	lights: .04,
}

var night = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#02040c")}, {.5, hex("#070d22")}, {.8, hex("#141a38")}, {.93, hex("#2c2442")}, {1, hex("#4a3044")}}
	t.far, t.farLit = hex("#11152a"), hex("#c89850")
	t.facade, t.slab, t.glass, t.lit = hex("#171c2c"), hex("#0d1120"), hex("#1a243c"), hex("#ffcf70")
	t.steel = hex("#6a6a7a")
	t.sidewalk, t.road, t.water, t.podium = hex("#2a2a36"), hex("#0e0e16"), hex("#050b1a"), hex("#30303e")
	t.smog, t.smogShade = hex("#5a5040"), hex("#2a241c")
	t.dust, t.dustShade = hex("#8a806e"), hex("#3a342c")
	t.glow = hex("#ffc27a")
	t.lights, t.stars = 1, .55
})

var dawn = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#121a3e")}, {.45, hex("#3a4a86")}, {.75, hex("#b98aa8")}, {.9, hex("#f3a98f")}, {1, hex("#ffd6a0")}}
	t.facade, t.glass = hex("#5a5070"), hex("#9a8ab0")
	t.far = hex("#4a4468")
	t.lights, t.stars = .45, .15
})

var morning = with(day, func(t *palette) {
	t.sky = []stop{{0, hex("#2b63c0")}, {.45, hex("#5c97df")}, {.8, hex("#a9cdef")}, {1, hex("#f4ecdc")}}
	t.lights = .1
})

var golden = with(day, func(t *palette) {
	t.sky = []stop{{0, hex("#2a5ab0")}, {.5, hex("#6e9ad8")}, {.8, hex("#e8c08a")}, {.93, hex("#ffb070")}, {1, hex("#ffd890")}}
	t.facade, t.glass, t.far = hex("#a08878"), hex("#f0c890"), hex("#a89098")
	t.glow = hex("#ffc070")
	t.lights = .2
})

// daylight is the light through a day by local hour, the mountains' timing; between two keys it blends.
var daylight = []struct {
	hour float64
	t    palette
}{{0, night}, {5, night}, {6.5, dawn}, {8, morning}, {12, day}, {16, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// paletteAt is the light at a local time; a light terminal only lifts the shadows.
func paletteAt(now time.Time, dark bool) palette {
	hour := 19.5
	if !now.IsZero() {
		hour = float64(now.Hour()) + float64(now.Minute())/60 + float64(now.Second())/3600
	}
	t := daylight[len(daylight)-1].t
	for i := 1; i < len(daylight); i++ {
		if a, b := daylight[i-1], daylight[i]; hour <= b.hour {
			t = blend(a.t, b.t, smooth(0, 1, (hour-a.hour)/(b.hour-a.hour)))
			break
		}
	}
	if !dark {
		t.slab = mix(t.slab, t.facade, .25)
		t.road = mix(t.road, t.sidewalk, .2)
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
	out.lights, out.stars = lerp(a.lights, b.lights, f), lerp(a.stars, b.stars, f)
	out.sky = nil
	for k := range 13 {
		v := float64(k) / 12
		out.sky = append(out.sky, stop{v, mix(a.skyAt(v), b.skyAt(v), f)})
	}
	return out
}

func (t *palette) colors() []*rgb {
	return []*rgb{&t.far, &t.farLit, &t.facade, &t.slab, &t.glass, &t.lit, &t.steel, &t.crane, &t.sidewalk, &t.road,
		&t.lane, &t.water, &t.lamp, &t.podium, &t.tail, &t.smog, &t.smogShade, &t.stormSky, &t.stormCloud, &t.stormTop,
		&t.rain, &t.bolt, &t.fire, &t.fireHot, &t.smoke, &t.kaiju, &t.kaijuGlow, &t.eye, &t.dust, &t.dustShade,
		&t.worker, &t.red, &t.green, &t.gold, &t.label, &t.rim, &t.glow}
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
