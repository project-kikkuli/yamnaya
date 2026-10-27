package mountains

// theme is the light of the scene at one time of day.
type theme struct {
	sky                            []stop
	snowLit, snowShade             rgb
	rockLit, rockShade             rgb
	forest, forestSnow, tree       rgb
	glow, rim                      rgb
	water                          rgb
	stormSky, stormCloud, stormTop rgb
	cloud, cloudShade              rgb
	lavaHot, lava, lavaDeep        rgb
	smoke, smokeDark               rgb
	climber, rope, flag, label     rgb
	bolt                           rgb
	green, gold                    rgb
	valley                         float64
	// stars is how bright the night sky is: stars, moon and lit windows show above zero.
	stars float64
}

type stop struct {
	at float64
	c  rgb
}

var dusk = theme{
	sky:     []stop{{0, hex("#050a1a")}, {.38, hex("#10204a")}, {.66, hex("#3b3f73")}, {.84, hex("#b4707a")}, {.94, hex("#ec9e64")}, {1, hex("#ffd08a")}},
	snowLit: hex("#f6c4ae"), snowShade: hex("#4a5a8e"),
	rockLit: hex("#74505e"), rockShade: hex("#151a33"),
	forest: hex("#0a1513"), forestSnow: hex("#33405e"), tree: hex("#03070a"),
	glow: hex("#ffad7e"), rim: hex("#ffe0b0"),
	water:    hex("#0b1830"),
	stormSky: hex("#1a1f2d"), stormCloud: hex("#23293a"), stormTop: hex("#5c6480"),
	cloud: hex("#d6a9b5"), cloudShade: hex("#544f78"),
	lavaHot: hex("#fff3a6"), lava: hex("#ff6418"), lavaDeep: hex("#9c1606"),
	smoke: hex("#b8b0b4"), smokeDark: hex("#2e2629"),
	climber: hex("#fff0b8"), rope: hex("#ff8a3d"), flag: hex("#ff4733"), label: hex("#ffe6c0"),
	bolt: hex("#f2ecff"), green: hex("#7ee08a"), gold: hex("#ffd36b"),
	valley: .5, stars: .8,
}

var day = theme{
	sky:     []stop{{0, hex("#1a56b8")}, {.4, hex("#3a83db")}, {.75, hex("#82b8ec")}, {.92, hex("#bcd9f3")}, {1, hex("#e6f0f8")}},
	snowLit: hex("#fbfdff"), snowShade: hex("#98b2d8"),
	rockLit: hex("#8c8b92"), rockShade: hex("#36425a"),
	forest: hex("#1a3326"), forestSnow: hex("#c4d3e2"), tree: hex("#0d1c15"),
	glow: hex("#ffe3a3"), rim: hex("#ffffff"),
	water:    hex("#245f9c"),
	stormSky: hex("#556070"), stormCloud: hex("#3f4755"), stormTop: hex("#8a94a3"),
	cloud: hex("#ffffff"), cloudShade: hex("#b9c6d6"),
	lavaHot: hex("#fff3a6"), lava: hex("#ff5a14"), lavaDeep: hex("#9c1606"),
	smoke: hex("#ecebee"), smokeDark: hex("#55525a"),
	climber: hex("#14213d"), rope: hex("#f0a030"), flag: hex("#e0241a"), label: hex("#ffffff"),
	bolt: hex("#ffffff"), green: hex("#1f9d3a"), gold: hex("#c98a00"),
	valley: .78,
}

func (t theme) skyAt(v float64) rgb {
	v = clamp(v, 0, 1)
	for i := 1; i < len(t.sky); i++ {
		if v <= t.sky[i].at {
			a, b := t.sky[i-1], t.sky[i]
			return mix(a.c, b.c, smooth(0, 1, (v-a.at)/(b.at-a.at)))
		}
	}
	return t.sky[len(t.sky)-1].c
}
