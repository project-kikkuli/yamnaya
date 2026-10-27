package subway

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type rgb = scene.RGB

func hex(s string) rgb            { return scene.Hex(s) }
func mix(a, b rgb, t float64) rgb { return scene.Mix(a, b, t) }

// palette is the map's ink at one time of day: a cream paper map by day, a dark one at night.
type palette struct {
	paper, district, rule, river rgb
	ink, name, faint, accent     rgb
	station, halo                rgb
	red, green, gold, amber      rgb
	cloud, storm, smoke, rain    rgb
	bolt                         rgb
	lines                        [9]rgb
}

var nightMap = palette{
	paper: hex("#0e1520"), district: hex("#141d2b"), rule: hex("#2c384c"), river: hex("#132a40"),
	ink: hex("#e6ebf2"), name: hex("#b9c3d1"), faint: hex("#7b889c"), accent: hex("#ffffff"),
	station: hex("#f5f7fb"), halo: hex("#3a4a66"),
	red: hex("#ff3b30"), green: hex("#34c759"), gold: hex("#ffcc33"), amber: hex("#ff9f0a"),
	cloud: hex("#8b95a8"), storm: hex("#3a404e"), smoke: hex("#4a4f5a"), rain: hex("#8fb4d9"),
	bolt:  hex("#fff6b0"),
	lines: [9]rgb{hex("#3d8bfd"), hex("#ff9500"), hex("#a46cf0"), hex("#ffd60a"), hex("#1fc2b4"), hex("#c08850"), hex("#d070e0"), hex("#5ac8fa"), hex("#6a74ff")},
}

var dayMap = palette{
	paper: hex("#f4f0e6"), district: hex("#ebe5d5"), rule: hex("#cfc6b0"), river: hex("#c9e0ef"),
	ink: hex("#141a26"), name: hex("#2e3646"), faint: hex("#6f6858"), accent: hex("#000000"),
	station: hex("#ffffff"), halo: hex("#d9cfb4"),
	red: hex("#d70015"), green: hex("#1a9e3f"), gold: hex("#c48a00"), amber: hex("#d97a00"),
	cloud: hex("#9aa3b2"), storm: hex("#4f5664"), smoke: hex("#7d7873"), rain: hex("#3f6ea3"),
	bolt:  hex("#e0a800"),
	lines: [9]rgb{hex("#0a64d8"), hex("#e07800"), hex("#7d3fc4"), hex("#e0b000"), hex("#00968a"), hex("#9a6230"), hex("#b04cc0"), hex("#2aa3dc"), hex("#3f4cc8")},
}

// dayness is how far toward the day map the clock is: the map turns over at half past six and seven.
func dayness(now time.Time) float64 {
	hour := 19.5
	if !now.IsZero() {
		hour = float64(now.Hour()) + float64(now.Minute())/60
	}
	return scene.Smooth(6.5, 7, hour) * scene.Smooth(19.5, 19, hour)
}

// paletteAt is the map at a local time; a light terminal lifts the night map so the board stays easy to find.
func paletteAt(now time.Time, dark bool) palette {
	d := dayness(now)
	a, b := nightMap, dayMap
	if !dark {
		a.paper, a.district, a.river = hex("#1c2636"), hex("#222d3f"), hex("#1b3550")
	}
	out := a
	pairs := [...][3]*rgb{
		{&out.paper, &a.paper, &b.paper}, {&out.district, &a.district, &b.district}, {&out.rule, &a.rule, &b.rule},
		{&out.river, &a.river, &b.river}, {&out.ink, &a.ink, &b.ink}, {&out.name, &a.name, &b.name},
		{&out.faint, &a.faint, &b.faint}, {&out.accent, &a.accent, &b.accent}, {&out.station, &a.station, &b.station},
		{&out.halo, &a.halo, &b.halo}, {&out.red, &a.red, &b.red}, {&out.green, &a.green, &b.green},
		{&out.gold, &a.gold, &b.gold}, {&out.amber, &a.amber, &b.amber}, {&out.cloud, &a.cloud, &b.cloud},
		{&out.storm, &a.storm, &b.storm}, {&out.smoke, &a.smoke, &b.smoke}, {&out.rain, &a.rain, &b.rain},
		{&out.bolt, &a.bolt, &b.bolt},
	}
	for _, p := range pairs {
		*p[0] = mix(*p[1], *p[2], d)
	}
	for i := range out.lines {
		out.lines[i] = mix(a.lines[i], b.lines[i], d)
	}
	return out
}

// lineColor is a line's own color; a line standing for several folded together is drawn in the faint ink.
func (p palette) lineColor(idx int) rgb {
	if idx < 0 {
		return p.faint
	}
	return p.lines[idx%len(p.lines)]
}

// carOn is the color of a train that reads against a band of line color.
func carOn(band rgb) rgb {
	if band.Luma() > .5 {
		return hex("#10151f")
	}
	return scene.White
}
