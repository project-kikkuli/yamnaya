package mountains

import "time"

var night = with(dusk, func(t *theme) {
	t.sky = []stop{{0, hex("#02040c")}, {.5, hex("#070d22")}, {.85, hex("#0f1a3a")}, {1, hex("#1a2748")}}
	t.snowLit, t.snowShade = hex("#a9bde0"), hex("#26335a")
	t.rockLit, t.rockShade = hex("#343c58"), hex("#0b0f1f")
	t.glow, t.rim = hex("#c9d6ff"), hex("#dfe8ff")
	t.water, t.cloud, t.cloudShade = hex("#050b1a"), hex("#3a4466"), hex("#1b2238")
	t.valley, t.stars = .38, 1
})

var dawn = with(dusk, func(t *theme) {
	t.sky = []stop{{0, hex("#121a3e")}, {.45, hex("#3a4a86")}, {.75, hex("#b98aa8")}, {.9, hex("#f3a98f")}, {1, hex("#ffd6a0")}}
	t.snowLit, t.snowShade = hex("#ffd2c8"), hex("#6a75a8")
	t.rockLit = hex("#8a6470")
	t.glow, t.rim = hex("#ffc2a8"), hex("#fff0e0")
	t.water, t.cloud, t.cloudShade = hex("#2a3560"), hex("#f2b8c0"), hex("#7a7aa8")
	t.valley, t.stars = .55, .25
})

var morning = with(day, func(t *theme) {
	t.sky = []stop{{0, hex("#2b63c0")}, {.45, hex("#5c97df")}, {.8, hex("#a9cdef")}, {1, hex("#f4ecdc")}}
	t.snowLit, t.valley = hex("#fffaf0"), .7
})

var golden = with(day, func(t *theme) {
	t.sky = []stop{{0, hex("#2a5ab0")}, {.5, hex("#6e9ad8")}, {.8, hex("#e8c08a")}, {.93, hex("#ffb070")}, {1, hex("#ffd890")}}
	t.snowLit, t.snowShade = hex("#ffdca0"), hex("#8a94c0")
	t.rockLit = hex("#a07a5a")
	t.glow, t.rim = hex("#ffc070"), hex("#fff0c0")
	t.water, t.cloud, t.cloudShade = hex("#3a5a8a"), hex("#ffe0b0"), hex("#c0a0a0")
	t.valley = .62
})

// daylight is the light through a day, by local hour; between two keys it blends.
var daylight = []struct {
	hour float64
	t    theme
}{{0, night}, {5, night}, {6.5, dawn}, {8, morning}, {12, day}, {16, day}, {18, golden}, {19.5, dusk}, {21, night}, {24, night}}

// themeAt is the light at a local time; a light terminal only lifts the shadows so the board stays easy to find.
func themeAt(now time.Time, dark bool) theme {
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
		t.rockShade = mix(t.rockShade, t.rockLit, .25)
		t.snowShade = mix(t.snowShade, t.snowLit, .2)
		t.valley = lerp(t.valley, 1, .3)
	}
	return t
}

func with(base theme, change func(*theme)) theme {
	base.sky = append([]stop(nil), base.sky...)
	change(&base)
	return base
}

// blend mixes every color and amount of two lights; the sky is resampled so gradients of any shape mix.
func blend(a, b theme, f float64) theme {
	out := a
	ca, cb, co := a.colors(), b.colors(), out.colors()
	for i := range co {
		*co[i] = mix(*ca[i], *cb[i], f)
	}
	out.valley, out.stars = lerp(a.valley, b.valley, f), lerp(a.stars, b.stars, f)
	out.sky = nil
	for k := range 13 {
		v := float64(k) / 12
		out.sky = append(out.sky, stop{v, mix(a.skyAt(v), b.skyAt(v), f)})
	}
	return out
}

func (t *theme) colors() []*rgb {
	return []*rgb{&t.snowLit, &t.snowShade, &t.rockLit, &t.rockShade, &t.forest, &t.forestSnow, &t.tree, &t.glow, &t.rim,
		&t.water, &t.stormSky, &t.stormCloud, &t.stormTop, &t.cloud, &t.cloudShade, &t.lavaHot, &t.lava, &t.lavaDeep,
		&t.smoke, &t.smokeDark, &t.climber, &t.rope, &t.flag, &t.label, &t.bolt, &t.green, &t.gold}
}
