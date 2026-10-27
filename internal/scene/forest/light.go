package forest

import "time"

// palette is the light of the forest at one time of day.
type palette struct {
	sky                                  []stop
	hillFar, hillNear                    rgb
	grass, grassLit, soil, litter        rgb
	moss, meadow, flower, hedge          rgb
	bark, barkShade, birch               rgb
	leaf, leafLit, leafShade, needle     rgb
	blossom, blossomHot                  rgb
	fog, fogShade                        rgb
	stormSky, stormCloud, stormTop, rain rgb
	bolt                                 rgb
	fire, fireHot, smoke, char           rgb
	agent, red, green, nest              rgb
	label, rim, beam, glow               rgb
	firefly, moon, sun                   rgb
	// lights is how dark it is, for fireflies and glows; stars how bright the sky is.
	lights, stars float64
}

type stop struct {
	at float64
	c  rgb
}

var dusk = palette{
	sky:     []stop{{0, hex("#0a0f24")}, {.4, hex("#1e2a55")}, {.7, hex("#5a4a78")}, {.86, hex("#c47a6a")}, {.95, hex("#f0a060")}, {1, hex("#ffd08a")}},
	hillFar: hex("#4a3a5e"), hillNear: hex("#2a2440"),
	grass: hex("#33502e"), grassLit: hex("#6a8448"), soil: hex("#241a14"), litter: hex("#46301e"),
	moss: hex("#263e24"), meadow: hex("#5e7a40"), flower: hex("#f0e6a0"), hedge: hex("#2c4628"),
	bark: hex("#6a4a34"), barkShade: hex("#2e2018"), birch: hex("#d8d0c0"),
	leaf: hex("#3c6436"), leafLit: hex("#9a9a50"), leafShade: hex("#1a3222"), needle: hex("#284a32"),
	blossom: hex("#f4c4d6"), blossomHot: hex("#fff0f4"),
	fog: hex("#c0b4c4"), fogShade: hex("#6e6680"),
	stormSky: hex("#1c2230"), stormCloud: hex("#252b3a"), stormTop: hex("#5c6480"), rain: hex("#94a4c4"),
	bolt: hex("#f2ecff"),
	fire: hex("#ff8a2a"), fireHot: hex("#ffe08a"), smoke: hex("#2e2628"), char: hex("#1a1210"),
	agent: hex("#fff0b8"), red: hex("#ff4733"), green: hex("#7ee08a"), nest: hex("#d8a868"),
	label: hex("#ffe6c0"), rim: hex("#ffe0b0"), beam: hex("#ffe0b0"), glow: hex("#ffad7e"),
	firefly: hex("#d8ff70"), moon: hex("#f4edd4"), sun: hex("#ffd890"),
	lights: .7, stars: .4,
}

var day = palette{
	sky:     []stop{{0, hex("#1a56b8")}, {.4, hex("#3a83db")}, {.75, hex("#82b8ec")}, {.92, hex("#c4dcf0")}, {1, hex("#e8f0e0")}},
	hillFar: hex("#8eaab0"), hillNear: hex("#5e8a78"),
	grass: hex("#4a8236"), grassLit: hex("#8cc060"), soil: hex("#5a4430"), litter: hex("#7a5a3a"),
	moss: hex("#386030"), meadow: hex("#9cc868"), flower: hex("#fff8c0"), hedge: hex("#3a6e34"),
	bark: hex("#7a5638"), barkShade: hex("#3e2a1c"), birch: hex("#f0ece0"),
	leaf: hex("#3e8a3a"), leafLit: hex("#8ac858"), leafShade: hex("#22502a"), needle: hex("#2e6a3e"),
	blossom: hex("#f8c8dc"), blossomHot: hex("#fff4f8"),
	fog: hex("#eef0f4"), fogShade: hex("#b0b8c0"),
	stormSky: hex("#556070"), stormCloud: hex("#3f4755"), stormTop: hex("#8a94a3"), rain: hex("#c0cad8"),
	bolt: hex("#ffffff"),
	fire: hex("#ff7a1a"), fireHot: hex("#ffd060"), smoke: hex("#55525a"), char: hex("#2a1e18"),
	agent: hex("#14213d"), red: hex("#e0241a"), green: hex("#1f9d3a"), nest: hex("#7a4a1a"),
	label: hex("#10202a"), rim: hex("#3a2400"), beam: hex("#fff6d8"), glow: hex("#ffe3a3"),
	firefly: hex("#d8ff70"), moon: hex("#f4edd4"), sun: hex("#fff2c0"),
	lights: .04,
}

var night = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#02040c")}, {.5, hex("#070d22")}, {.8, hex("#141a38")}, {.93, hex("#1e2440")}, {1, hex("#2a2e48")}}
	t.hillFar, t.hillNear = hex("#141a32"), hex("#0c1020")
	t.grass, t.grassLit, t.soil, t.litter = hex("#18281c"), hex("#2a4030"), hex("#0e0c0a"), hex("#1a140e")
	t.moss, t.meadow, t.hedge = hex("#122016"), hex("#22362a"), hex("#14241a")
	t.bark, t.barkShade, t.birch = hex("#3a3028"), hex("#140f0c"), hex("#8a8a90")
	t.leaf, t.leafLit, t.leafShade, t.needle = hex("#1c3428"), hex("#36543e"), hex("#0c1a14"), hex("#142c20")
	t.blossom, t.blossomHot = hex("#c8acc4"), hex("#ece0f0")
	t.fog, t.fogShade = hex("#4e566e"), hex("#262a3a")
	t.glow = hex("#ffc27a")
	t.lights, t.stars = 1, .6
})

var dawn = with(dusk, func(t *palette) {
	t.sky = []stop{{0, hex("#121a3e")}, {.45, hex("#3a4a86")}, {.75, hex("#b98aa8")}, {.9, hex("#f3a98f")}, {1, hex("#ffd6a0")}}
	t.hillFar, t.leafLit = hex("#6a5a80"), hex("#a8946a")
	t.lights, t.stars = .45, .15
})

var morning = with(day, func(t *palette) {
	t.sky = []stop{{0, hex("#2b63c0")}, {.45, hex("#5c97df")}, {.8, hex("#a9cdef")}, {1, hex("#f4ecdc")}}
	t.lights = .1
})

var golden = with(day, func(t *palette) {
	t.sky = []stop{{0, hex("#2a5ab0")}, {.5, hex("#6e9ad8")}, {.8, hex("#e8c08a")}, {.93, hex("#ffb070")}, {1, hex("#ffd890")}}
	t.hillFar, t.leafLit = hex("#a89098"), hex("#c8b450")
	t.glow, t.sun = hex("#ffc070"), hex("#ffd890")
	t.lights = .2
})

// daylight is the light through a day by local hour, the mountains' timing; between two keys it blends.
var daylight = []struct {
	hour float64
	t    palette
}{{0, night}, {5, night}, {6.5, dawn}, {8, morning}, {12, day}, {16, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// hourOf is the local hour as a fraction; a zero time is dusk.
func hourOf(now time.Time) float64 {
	if now.IsZero() {
		return 19.5
	}
	return float64(now.Hour()) + float64(now.Minute())/60 + float64(now.Second())/3600
}

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
		t.leafShade = mix(t.leafShade, t.leaf, .25)
		t.barkShade = mix(t.barkShade, t.bark, .2)
		t.soil = mix(t.soil, t.litter, .25)
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
	return []*rgb{&t.hillFar, &t.hillNear, &t.grass, &t.grassLit, &t.soil, &t.litter, &t.moss, &t.meadow, &t.flower,
		&t.hedge, &t.bark, &t.barkShade, &t.birch, &t.leaf, &t.leafLit, &t.leafShade, &t.needle, &t.blossom,
		&t.blossomHot, &t.fog, &t.fogShade, &t.stormSky, &t.stormCloud, &t.stormTop, &t.rain, &t.bolt, &t.fire,
		&t.fireHot, &t.smoke, &t.char, &t.agent, &t.red, &t.green, &t.nest, &t.label, &t.rim, &t.beam, &t.glow,
		&t.firefly, &t.moon, &t.sun}
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
