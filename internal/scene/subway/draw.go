package subway

import (
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

var letters = func() (out []string) {
	for _, r := range "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz23456789" {
		out = append(out, string(r))
	}
	return out
}()

func letterOf(idx int) string {
	if idx < 0 {
		return "+"
	}
	return letters[idx%len(letters)]
}

// band is the color a line's track is drawn in, its weather and its kill showing in it.
func (pl palette) band(g *geo) rgb {
	c := pl.lineColor(g.l.idx)
	switch g.l.weather {
	case scene.Clouds:
		c = mix(c, pl.faint, .35)
	case scene.Storm:
		c = mix(c, pl.storm, .3)
	case scene.Eruption:
		c = mix(c, pl.faint, .6)
	}
	return mix(c, pl.paper, g.l.dying*.6)
}

// glow lights the paper round the selected line.
func (pl palette) glow(s *sheet, g *geo) {
	for x := g.tagX; x < g.x1+1; x++ {
		py := g.py(max(x, g.x0))
		for y := py - 2; y < py+g.th+2; y++ {
			s.blend(x, y, pl.halo, .8)
		}
	}
}

// track draws a line's band: solid where it is built, thin where it is still being built, broken where
// service is suspended, and gone past where a kill has erased it.
func (pl palette) track(s *sheet, g *geo, own func(x, y int)) {
	c := pl.band(g)
	thin := mix(c, pl.paper, .35)
	built, shown := g.built(), g.shown()
	bend := "╲"
	if g.dir < 0 {
		bend = "╱"
	}
	for x := g.x0; x < shown; x++ {
		py, row := g.py(x), g.row(x)
		glyph := "━"
		switch {
		case g.l.weather == scene.Eruption && (x-g.x0)%4 == 3:
			glyph = " "
		case x < built:
			for t := range g.th {
				s.pix(x, py+t, c)
			}
		default:
			s.pix(x, py+g.th-1, thin)
			if g.bent(x) {
				s.pix(x, py+g.th-1-g.dir, thin)
			}
			glyph = "┄"
		}
		if glyph != " " && g.jog > 0 && x >= g.jogX && x < g.jogX+2*g.jog {
			glyph = bend
		}
		s.shape(x, row, glyph)
		if own != nil {
			own(x, py/2)
			own(x, (py+g.th-1)/2)
		}
	}
}

// tag writes a line's name tag: the selection marker, its lettered badge, its name, how far along it is and
// the trains resting in its depot.
func (pl palette) tag(s *sheet, g *geo, own func(x, y int)) {
	if g.l.dying > .85 {
		return
	}
	y, x := g.left, g.tagX
	fade := func(c rgb) rgb { return mix(c, pl.paper, g.l.dying) }
	if g.l.selected && g.l.dying == 0 {
		s.over(x, y, "▸", pl.accent)
	}
	c := fade(pl.lineColor(g.l.idx))
	on := carOn(c)
	s.put(x+1, y, " ", on, c)
	s.put(x+2, y, letterOf(g.l.idx), on, c)
	s.put(x+3, y, " ", on, c)
	name := pl.name
	switch {
	case g.l.dying > 0:
	case g.l.needsYou:
		name = pl.red
	case g.l.selected:
		name = pl.accent
	}
	s.text(x+5, y, truncate(g.l.name, g.nameW), fade(name), g.nameW)
	pct := itoa(int(g.l.progress*100+.5)) + "%"
	s.text(x+5+g.nameW+1+4-len(pct), y, pct, fade(pl.faint), 4)
	if n := stanceCount(g.l.team, scene.Resting); n > 0 && g.l.dying == 0 {
		s.over(x+5+g.nameW+6, y, "▭", pl.name)
		s.text(x+5+g.nameW+7, y, itoa(min(n, 9)), pl.faint, 1)
	}
	if own != nil {
		for k := x; k < g.x0; k++ {
			own(k, y)
		}
	}
}

// stops draws what stands on the track: red trains waiting on you, then each pull request as a station, then
// the terminus. A line being killed shows no alerts, and its stations dim and go as the track is erased.
func (pl palette) stops(s *sheet, g *geo) {
	shown, dying := g.shown(), g.l.dying
	bg := mix(pl.station, pl.paper, dying*.7)
	if dying == 0 {
		for k := range stanceCount(g.l.team, scene.Waiting) {
			want := g.x0 + 2 + int((.2+.6*scene.Unit(g.seed, 40+k))*float64(max(1, g.span()-6)))
			if x := g.slot(s, want, 3); x >= 0 {
				pl.waiting(s, g, x)
			}
		}
	}
	line := mix(pl.lineColor(g.l.idx), pl.paper, dying*.6)
	for _, c := range g.l.camps {
		x := g.slot(s, g.stationX(c.Stage), 1)
		if x < 0 || x >= shown {
			continue
		}
		glyph, fg := "○", line
		switch c.State {
		case scene.Green:
			glyph, fg = "◉", pl.green
		case scene.Failing:
			glyph, fg = "✕", pl.red
		case scene.Merged:
			glyph = "●"
		}
		if dying > 0 {
			fg = mix(pl.faint, bg, dying*.5)
		}
		f, b := scene.Readable(fg, bg)
		s.put(x, g.row(x), glyph, f, b)
	}
	if shown < g.x1 {
		return
	}
	x := g.x1 - 1
	switch g.l.weather {
	case scene.Summit:
		s.put(x, g.row(x), "★", hex("#141a26"), pl.gold)
	default:
		s.put(x, g.row(x), "◎", hex("#141a26"), bg)
	}
	if g.l.weather == scene.Eruption {
		s.over(g.x0, g.row(g.x0), "╳", pl.amber)
	}
}

// waiting draws a red train stopped on the track with a white rim, so it reads on any line color.
func (pl palette) waiting(s *sheet, g *geo, x int) {
	rim := scene.Backing(pl.red, pl.station)
	s.put(x, g.row(x), "▐", pl.red, rim)
	s.put(x+1, g.row(x+1), "!", scene.White, scene.Backing(scene.White, pl.red))
	s.put(x+2, g.row(x+2), "▌", pl.red, rim)
}

// interchange draws a chief's station: a white capsule across the lines it joins.
func (pl palette) interchange(s *sheet, gs []geo, h hub, x int, own func(x, y int)) {
	if len(gs) == 0 {
		return
	}
	top, bottom := gs[0].row(x), gs[len(gs)-1].row(x)
	if top > bottom {
		top, bottom = bottom, top
	}
	ink, rim := hex("#141a26"), pl.station
	if h.selected {
		rim = pl.gold
	}
	rows := map[int]bool{}
	for i := range gs {
		if gs[i].l.dying < 1 {
			rows[gs[i].row(x)] = true
		}
	}
	for y := top; y <= bottom; y++ {
		if rows[y] {
			s.put(x, y, "◉", ink, rim)
		} else {
			s.put(x, y, "┃", ink, rim)
		}
		if own != nil {
			own(x, y)
		}
	}
}
