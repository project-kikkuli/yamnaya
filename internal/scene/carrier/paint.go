package carrier

import (
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	tail, body, nose = "◣", "━", "►"
	busyA, busyB     = "Ϋ", "Ψ"
	rester           = "♙"
	catapult         = "»"
	pennant          = "⚑"
)

var waveSeed = scene.SeedOf("carrier waves")

func paintBackdrop(key backdropKey) *backdrop {
	w, h := key.w, key.h
	b := &backdrop{key: key, lt: lightAt(key.at, key.dark), bg: make([]rgb, w*h), fg: make([]rgb, w*h), glyph: make([]string, w*h), sea: min(key.sea, h)}
	lt := b.lt
	seed := scene.SeedOf("carrier stars")
	for y := range h {
		for x := range w {
			i := y*w + x
			b.glyph[i] = " "
			if y < b.sea {
				bg := scene.Mix(lt.zenith, lt.horizon, float64(y)/float64(max(1, b.sea-1)))
				b.bg[i] = bg
				if lt.stars > .05 && scene.Unit(seed, i) < .018*lt.stars {
					b.glyph[i], b.fg[i] = "·", scene.Mix(bg, scene.White, .35+.4*lt.stars*scene.Unit(seed, i+w*h))
				}
				continue
			}
			bg := scene.Mix(lt.seaFar, lt.seaNear, scene.Smooth(0, 1, float64(y-b.sea)/float64(max(1, h-b.sea))))
			b.bg[i], b.fg[i] = bg, scene.Mix(bg, lt.foam, .4)
		}
	}
	hour := hourOf(key.at)
	t := (hour - 6) / 13.5
	if t < 0 || t > 1 {
		t = math.Mod(hour-19.5+24, 24) / 10.5
	}
	if rows := b.sea - 4; rows >= 2 {
		x, y := 2+int(t*float64(w-5)), int((1-math.Sin(math.Pi*t))*float64(rows-1))
		if i := y*w + x; x >= 0 && x < w && y >= 0 {
			b.glyph[i], b.fg[i] = "●", lt.sun
		}
	}
	return b
}

// painter draws one frame: the backdrop, then each carrier and fleet over it.
type painter struct {
	c        *scene.Canvas
	bd       *backdrop
	lt       light
	l        layout
	frame    int
	box      scene.Rect
	w, h     int
	occ      []bool
	textBuf  []byte
	hiddenAt int
}

func (p *painter) set(x, y int, fg, bg rgb, glyph string) {
	if x < 0 || y < 0 || x >= p.w || y >= p.h || p.box.Has(x, y) {
		return
	}
	if scene.Colors == scene.NoColor {
		p.c.Plain(x, y, glyph)
		return
	}
	p.c.Set(x, y, fg, bg, glyph)
}

// fill paints x, y solid in col, drawn as plain under NoColor so the shape still shows.
func (p *painter) fill(x, y int, col rgb, plain string) {
	if scene.Colors == scene.NoColor {
		p.set(x, y, col, col, plain)
		return
	}
	p.set(x, y, col, col, " ")
}

func (p *painter) bg(x, y int) rgb {
	x, y = min(max(0, x), p.w-1), min(max(0, y), p.h-1)
	return p.bd.bg[y*p.w+x]
}

// air draws glyph over the sky or sea at x, y, moved away from it until the two contrast.
func (p *painter) air(x, y int, fg rgb, glyph string) {
	bg := p.bg(x, y)
	p.set(x, y, scene.Readable(fg, bg), bg, glyph)
}

// text writes s from x on row y, at most n cells, over bg when given and else over the sky or sea.
func (p *painter) text(x, y, n int, s string, fg rgb, bg *rgb) int {
	drawn := 0
	for _, r := range s {
		if drawn >= n {
			break
		}
		if bg != nil {
			p.set(x+drawn, y, fg, *bg, scene.Glyph(r))
		} else {
			p.air(x+drawn, y, fg, scene.Glyph(r))
		}
		drawn++
	}
	return drawn
}

// sea lays the backdrop down with its waves moving at the ambient beat.
func (p *painter) sea() {
	phase := p.frame / 10
	for y := range p.h {
		for x := range p.w {
			if p.box.Has(x, y) {
				continue
			}
			i := y*p.w + x
			glyph := p.bd.glyph[i]
			if y >= p.bd.sea && glyph == " " {
				depth := y - p.bd.sea
				if v := scene.Spread(waveSeed+uint64(y), x+phase*(1+y%3)); v%100 < uint64(4+min(10, depth)) {
					glyph = "~"
					if v>>8&1 == 1 {
						glyph = "∼"
					}
				}
			}
			p.set(x, y, p.bd.fg[i], p.bd.bg[i], glyph)
		}
	}
}

func (p *painter) ship(sh *ship) {
	lt, y0 := p.lt, p.l.row(sh.band)
	x0, x1 := sh.x, sh.x+sh.w
	for x := x0; x < x1; x++ {
		p.fill(x, y0+2, lt.deck, "▁")
		if x < x1-1 {
			p.fill(x, y0+3, lt.hull, "█")
		}
	}
	p.set(x1-1, y0+3, lt.hull, p.bg(x1-1, y0+3), "◤")
	p.set(x0, y0+4, lt.hull, p.bg(x0, y0+4), "◥")
	for x := x0 + 1; x < x1-2; x++ {
		p.set(x, y0+4, lt.hull, p.bg(x, y0+4), "▀")
	}
	p.set(x1-2, y0+4, lt.hull, p.bg(x1-2, y0+4), "◤")
	p.island(sh, y0)
	for k, g := range [...]string{"≈", "~", "∼"} {
		p.air(x0-1-k, y0+4, scene.Mix(lt.foam, p.bg(x0-1-k, y0+4), .3*float64(k)), g)
	}
	name, fg := sh.name, lt.hullText
	if sh.selected {
		name, fg = "▾ "+sh.name, lt.label
	}
	if n := utf8.RuneCountInString(name); name != "" {
		room := sh.w - 3
		p.text(x0+1+max(0, (room-n)/2), y0+3, room, name, fg, &lt.hull)
	}
	for k := range sh.squads {
		p.squad(&sh.squads[k], y0)
	}
}

// island is the carrier's superstructure aft, standing in the group's weather.
func (p *painter) island(sh *ship, y0 int) {
	lt, ix := p.lt, sh.x+1
	for x := ix; x < ix+3; x++ {
		p.fill(x, y0+2, lt.island, "█")
		p.fill(x, y0+1, lt.island, "█")
	}
	if lt.stars > .3 {
		p.set(ix, y0+1, lt.window, lt.island, "▪")
		p.set(ix+2, y0+1, lt.window, lt.island, "▪")
	}
	p.air(ix+1, y0, lt.island, "┼")
	switch sh.sky {
	case scene.Summit:
		p.air(ix+1, y0, lt.gold, pennant)
	case scene.Eruption:
		for x := ix; x < ix+3; x++ {
			p.air(x, y0, lt.smoke, "▒")
		}
		p.set(ix+1, y0+1, p.flame(ix), lt.island, "▲")
	case scene.Storm:
		for x := ix - 1; x < ix+4; x++ {
			if (x+p.frame)%2 == 0 {
				p.air(x, y0, lt.rain, "╱")
			}
		}
	case scene.Clouds:
		for x := ix - 1; x < ix+4; x++ {
			p.air(x, y0, lt.cloud, "▒")
		}
	}
}

func (p *painter) flame(x int) rgb {
	if (x+p.frame)%2 == 0 {
		return p.lt.fireHot
	}
	return p.lt.fire
}

func (p *painter) free(x, n, x1 int) bool {
	if x < 0 || x+n > x1 {
		return false
	}
	for k := x; k < x+n; k++ {
		if p.occ[k] {
			return false
		}
	}
	return true
}

func (p *painter) take(x, n int) {
	for k := x; k < x+n && k < p.w; k++ {
		p.occ[k] = true
	}
}

// spot is the free run of n deck cells nearest want in x0..x1, or -1.
func (p *painter) spot(want, n, x0, x1 int) int {
	for d := 0; d <= x1-x0; d++ {
		for _, x := range [2]int{want - d, want + d} {
			if x >= x0 && p.free(x, n, x1) {
				return x
			}
		}
	}
	return -1
}

func (p *painter) plane(x, y int, fg, bg rgb, onAir bool) {
	if onAir {
		p.air(x, y, fg, tail)
		p.air(x+1, y, fg, body)
		p.air(x+2, y, fg, nose)
		return
	}
	p.set(x, y, fg, bg, tail)
	p.set(x+1, y, fg, bg, body)
	p.set(x+2, y, fg, bg, nose)
}

// squad draws a squadron on its stretch of deck: weather, catapult, crew, planes, alerts and its name.
func (p *painter) squad(q *squad, y0 int) {
	lt := p.lt
	x0, x1 := q.x, q.x+q.w
	deck := x1 - 1
	if q.dying > 0 {
		p.kill(q, y0)
		return
	}
	roll := 0
	switch q.weather {
	case scene.Clouds:
		seed := scene.SeedOf(q.id)
		for x := x0; x < x1; x++ {
			v := scene.Noise(seed, float64(x)*.4+float64(p.frame/10)*.3)
			g := "░"
			if v > .66 {
				g = "▓"
			} else if v > .33 {
				g = "▒"
			}
			p.air(x, y0+1, lt.cloud, g)
		}
	case scene.Storm:
		roll = p.frame / 4 % 2
		for x := x0; x < x1; x++ {
			if (x+p.frame)%3 == 0 {
				p.air(x, y0+1, lt.rain, "╱")
			}
			if (x+p.frame/4)%2 == 0 {
				p.air(x, y0+5, scene.Mix(lt.foam, scene.White, .5), "^")
			}
		}
	case scene.Eruption:
		for x := x0; x < x1; x++ {
			if (x*7+p.frame)%3 != 0 {
				p.air(x, y0+1, lt.smoke, "▒")
			}
		}
	}
	cat, catFg := catapult, scene.Mix(lt.deck, lt.plane, .5)
	if q.weather == scene.Summit {
		cat, catFg = pennant, lt.gold
	}
	p.set(deck, y0+2, catFg, lt.deck, cat)
	for x := x0; x < deck; x++ {
		p.occ[x] = false
	}
	at := x0 + roll
	for _, a := range q.team {
		if a.Stance == scene.Waiting {
			if x := p.spot(at, 1, x0, deck); x >= 0 {
				p.set(x, y0+2, lt.red, lt.deck, busyA)
				p.take(x, 1)
			}
		}
	}
	span := max(0, deck-x0-3)
	hidden := 0
	for stage := 5; stage >= 0; stage-- {
		for _, c := range q.camps {
			if airborne(c) || min(max(c.Stage, 0), 5) != stage {
				continue
			}
			x := p.spot(x0+roll+int(math.Round(float64(stage)/5*float64(span))), 3, x0, deck)
			if x < 0 {
				hidden++
				continue
			}
			fg := lt.plane
			switch c.State {
			case scene.Green:
				fg = lt.green
			case scene.Failing:
				fg = lt.red
			}
			p.plane(x, y0+2, fg, lt.deck, false)
			p.take(x, 3)
		}
	}
	if q.weather == scene.Eruption {
		for x := x0; x < deck; x++ {
			if !p.occ[x] && (x+p.frame)%2 == 0 {
				p.set(x, y0+2, p.flame(x), lt.deck, "▲")
			}
		}
	} else {
		k := 0
		for _, a := range q.team {
			if a.Stance != scene.Climbing {
				continue
			}
			if x := p.spot(at, 1, x0, deck); x >= 0 {
				g := busyA
				if (p.frame/4+k)%2 == 1 {
					g = busyB
				}
				p.set(x, y0+2, lt.crew, lt.deck, g)
				p.take(x, 1)
			}
			k++
		}
		for _, a := range q.team {
			if a.Stance != scene.Resting {
				continue
			}
			if x := p.spot(at, 1, x0, deck); x >= 0 {
				p.set(x, y0+2, scene.Mix(lt.deck, lt.plane, .6), lt.deck, rester)
				p.take(x, 1)
			}
		}
	}
	p.sky(q, y0, hidden)
	p.label(q, y0)
}

// sky draws what flies over a squadron: its merged planes, a flyover when it is done, and its signal flags.
func (p *painter) sky(q *squad, y0, hidden int) {
	lt := p.lt
	x0, x1 := q.x, q.x+q.w
	up := 0
	for _, c := range q.camps {
		if airborne(c) {
			up++
		}
	}
	slots := max(0, (q.w-3)/4)
	for k := range min(up, slots) {
		x := x1 - 4 - 4*k - (p.frame/10+k)%2
		if k == slots-1 && up > slots {
			p.text(x, y0+1, 4, p.count('+', up-k), lt.gold, nil)
			break
		}
		p.plane(x, y0+1, lt.gold, lt.gold, true)
	}
	if hidden > 0 {
		s := p.count('+', hidden)
		p.text(x1-1-len(s), y0+1, len(s), s, lt.plane, nil)
	}
	if q.weather == scene.Summit {
		x := x0 + p.frame/2%max(1, q.w-3)
		for k := range 3 {
			p.air(x+k, y0+1, lt.label, nose)
		}
	}
	if q.needs {
		a, b := "▚", "▞"
		if p.frame/4%2 == 1 {
			a, b = b, a
		}
		p.set(x0, y0+1, lt.red, p.bg(x0, y0+1), a)
		p.set(x0+1, y0+1, lt.red, p.bg(x0+1, y0+1), b)
	}
}

// count is a sign and n, written into a buffer the painter reuses.
func (p *painter) count(sign byte, n int) string {
	p.textBuf = strconv.AppendInt(append(p.textBuf[:0], sign), int64(n), 10)
	return string(p.textBuf)
}

// label names a squadron with how far along it is; red while it needs the owner, lit and marked when selected.
func (p *painter) label(q *squad, y0 int) {
	lt := p.lt
	fg := scene.Mix(lt.label, p.bg(q.x, y0), .2)
	if q.selected {
		fg = lt.label
	}
	if q.needs {
		fg = lt.red
	}
	if q.dying > 0 {
		fg = scene.Mix(fg, p.bg(q.x, y0), q.dying)
	}
	x, room := q.x, q.w-1
	if q.selected {
		p.air(x, y0, lt.label, "▾")
		x, room = x+1, room-1
	}
	pct := strconv.Itoa(int(math.Round(q.progress * 100)))
	name := q.name
	if q.units > 1 && utf8.RuneCountInString(name)+len(pct)+2 > room {
		name = strconv.Itoa(q.units) + " sq"
	}
	if n := utf8.RuneCountInString(name); n+len(pct)+2 > room {
		name = string([]rune(name)[:max(0, min(n, room-len(pct)-3))]) + "…"
	}
	p.textBuf = append(append(append(append(p.textBuf[:0], name...), ' '), pct...), '%')
	if q.needs {
		p.recolor(x, y0, room, string(p.textBuf), fg)
		return
	}
	p.text(x, y0, room, string(p.textBuf), fg, nil)
}

// recolor writes red text over the sky as it is, without the contrast shift that would turn it pink.
func (p *painter) recolor(x, y, n int, s string, fg rgb) {
	k := 0
	for _, r := range s {
		if k >= n {
			break
		}
		p.set(x+k, y, fg, p.bg(x+k, y), scene.Glyph(r))
		k++
	}
}

// kill pushes the squadron's lead plane over the side: it slides off the deck, falls past the hull and splashes.
func (p *painter) kill(q *squad, y0 int) {
	lt, d := p.lt, q.dying
	px := q.x + q.w/2 - 1
	switch {
	case d < .3:
		p.plane(px+int(d/.3*3), y0+2, lt.plane, lt.deck, false)
	case d < .45:
		p.plane(px+3, y0+3, lt.plane, lt.hull, false)
	case d < .6:
		p.set(px+4, y0+4, lt.plane, p.bg(px+4, y0+4), "▼")
	default:
		s := (d - .6) / .4
		cx := px + 4
		reach := 1 + int(s*4)
		for x := cx - reach; x <= cx+reach; x++ {
			p.air(x, y0+5, scene.Mix(lt.foam, p.bg(x, y0+5), s*.6), "≈")
		}
		if s < .6 {
			p.air(cx-1, y0+4, lt.foam, "'")
			p.air(cx, y0+4, lt.foam, "*")
			p.air(cx+1, y0+4, lt.foam, "'")
		}
		if s < .3 {
			p.set(cx, y0+3, lt.foam, lt.hull, "·")
		}
	}
	p.label(q, y0)
}

// bracket draws a fleet's line under its carriers in formation, named for its chief.
func (p *painter) bracket(f bracket) {
	lt, y := p.lt, p.l.row(f.band)+5
	fg := scene.Mix(lt.label, p.bg(f.x0, y), .35)
	for x := f.x0; x < f.x1; x++ {
		g := "─"
		switch x {
		case f.x0:
			g = "╰"
		case f.x1 - 1:
			g = "╯"
		}
		p.air(x, y, fg, g)
	}
	name := " " + f.name + " fleet "
	if f.selected {
		name, fg = " ▾ "+f.name+" fleet ", lt.label
	}
	n, room := utf8.RuneCountInString(name), f.x1-f.x0-4
	if n > room && room > 2 {
		name, n = string([]rune(name)[:room-2])+"… ", room
	}
	p.text(f.x0+2+max(0, (room-n)/2), y, room, name, fg, nil)
}
