package moai

import (
	"math"
	"strconv"
	"unicode/utf8"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	busyA, busyB = "λ", "ʎ"
	rester       = "∩"
	stone        = "■"
	cracked      = "▞"
	pukao        = "●"
	fireA, fireB = "✸", "✶"
	fireBase     = "▲"
	topknot      = "▄"
)

// figure is a finished moai seen from the front, two pixels to a cell over three rows: L lit stone, S stone,
// D shadow, E an eye socket, . the sky behind.
var figure = [6]string{
	".LLS.",
	"LELES",
	"LLLSS",
	"SDDDS",
	".LSS.",
	"LLSSD",
}

var (
	waveSeed  = scene.SeedOf("moai waves")
	roughSeed = scene.SeedOf("moai rough stone")
)

func paintBackdrop(key backdropKey) *backdrop {
	w, h := key.w, key.h
	b := &backdrop{key: key, lt: lightAt(key.at, key.dark), bg: make([]rgb, w*h), fg: make([]rgb, w*h), glyph: make([]string, w*h)}
	lt := b.lt
	b.sea = max(min(key.horizon+2, h), h-max(2, h/8))
	starSeed, hillSeed, tuftSeed := scene.SeedOf("moai stars"), scene.SeedOf("moai hills"), scene.SeedOf("moai grass")
	horizon := min(key.horizon, b.sea)
	for x := range w {
		crest := horizon - int(math.Round(scene.Fbm(hillSeed, float64(x)*.035, 3)*4))
		for y := range h {
			i := y*w + x
			b.glyph[i] = " "
			switch {
			case y < crest:
				bg := scene.Mix(lt.zenith, lt.horizon, scene.Smooth(0, 1, float64(y)/float64(max(1, horizon))))
				b.bg[i] = bg
				// The Milky Way runs from the upper left down to the right, denser near its line.
				band := math.Abs(float64(y)-float64(x)*.18-float64(horizon)*.1) / float64(max(1, horizon))
				density := .016 + .05*math.Max(0, 1-band*4)
				if lt.stars > .05 {
					b.bg[i] = scene.Mix(bg, lt.star, .05*lt.stars*math.Max(0, 1-band*5)*scene.Noise2(starSeed, float64(x)*.3, float64(y)*.6))
				}
				if v := scene.Unit(starSeed, i); lt.stars > .05 && v < density*lt.stars {
					b.glyph[i], b.fg[i] = "·", scene.Mix(b.bg[i], lt.star, .35+.5*lt.stars*scene.Unit(starSeed, i+w*h))
					if v < .002*lt.stars {
						b.glyph[i] = "✦"
					}
				}
			case y < b.sea:
				depth := scene.Smooth(0, 1, float64(y-crest)/float64(max(1, b.sea-crest)))
				bg := scene.Mix(lt.hillFar, lt.hillNear, depth*2)
				if depth > .5 {
					bg = scene.Mix(lt.hillNear, lt.grass, (depth-.5)*2)
				}
				b.bg[i] = bg
				if v := scene.Unit(tuftSeed, i); v < .06 {
					b.glyph[i], b.fg[i] = "ʷ", scene.Mix(bg, lt.green, .25)
					if v < .02 {
						b.glyph[i] = ","
					}
				}
			case y == b.sea:
				b.bg[i], b.fg[i] = lt.seaFar, lt.foam
				if scene.Unit(waveSeed, i) < .5 {
					b.glyph[i] = "▁"
				}
			default:
				bg := scene.Mix(lt.seaFar, lt.seaNear, scene.Smooth(0, 1, float64(y-b.sea)/float64(max(1, h-b.sea))))
				b.bg[i], b.fg[i] = bg, scene.Mix(bg, lt.foam, .4)
			}
		}
	}
	hour := hourOf(key.at)
	t := (hour - 6) / 13.5
	if t < 0 || t > 1 {
		t = math.Mod(hour-19.5+24, 24) / 10.5
	}
	if rows := horizon - 4; rows >= 2 {
		x, y := 2+int(t*float64(w-5)), int((1-math.Sin(math.Pi*t))*float64(rows-1))
		if i := y*w + x; x >= 0 && x < w && y >= 0 {
			b.glyph[i], b.fg[i] = "●", lt.sun
		}
	}
	return b
}

// painter draws one frame: the backdrop, then each ahu and ceremonial site over it.
type painter struct {
	c       *scene.Canvas
	bd      *backdrop
	lt      light
	l       layout
	frame   int
	box     scene.Rect
	w, h    int
	occ     []bool
	textBuf []byte
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

// px draws a cell of two pixels, each the given color when on and the backdrop when off.
func (p *painter) px(x, y int, top, bot rgb, topOn, botOn bool) {
	switch {
	case topOn && botOn && top == bot:
		p.fill(x, y, top, "█")
	case topOn && botOn:
		p.set(x, y, top, bot, "▀")
	case topOn:
		p.set(x, y, top, p.bg(x, y), "▀")
	case botOn:
		p.set(x, y, bot, p.bg(x, y), "▄")
	}
}

func (p *painter) bg(x, y int) rgb {
	x, y = min(max(0, x), p.w-1), min(max(0, y), p.h-1)
	return p.bd.bg[y*p.w+x]
}

// air draws glyph over the backdrop at x, y, the two moved apart until they contrast.
func (p *painter) air(x, y int, fg rgb, glyph string) {
	f, b := scene.Readable(fg, p.bg(x, y))
	p.set(x, y, f, b, glyph)
}

// signal draws a glyph whose color is its meaning on the backdrop darkened or lightened just enough to read.
func (p *painter) signal(x, y int, fg rgb, glyph string) {
	p.set(x, y, fg, scene.Backing(fg, p.bg(x, y)), glyph)
}

// text writes s from x on row y, at most n cells, over bg when given and else over the backdrop.
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

// ground lays the backdrop down with the sea's waves moving at the ambient beat.
func (p *painter) ground() {
	phase := p.frame / 10
	for y := range p.h {
		for x := range p.w {
			if p.box.Has(x, y) {
				continue
			}
			i := y*p.w + x
			glyph := p.bd.glyph[i]
			if y > p.bd.sea && glyph == " " {
				if v := scene.Spread(waveSeed+uint64(y), x+phase*(1+y%3)); v%100 < uint64(5+min(10, y-p.bd.sea)) {
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

// ahu draws a stone platform with the group's name cut in it, the cove of sea at its foot, and its moai.
func (p *painter) ahu(a *ahu) {
	lt, y0 := p.lt, p.l.row(a.band)
	x0, x1 := a.x, a.x+a.w
	phase := p.frame / 10
	for x := x0 - 1; x <= x1; x++ {
		bg := scene.Mix(lt.seaFar, lt.seaNear, .4)
		g := " "
		if v := scene.Spread(waveSeed, x+phase); v%4 == 0 {
			g = "~"
		}
		if x == x0-1 || x == x1 {
			p.set(x, y0+7, bg, p.bg(x, y0+7), "▄")
			continue
		}
		p.set(x, y0+7, scene.Mix(bg, lt.foam, .45), bg, g)
	}
	for x := x0; x < x1; x++ {
		p.fill(x, y0+5, lt.ahu, "▆")
	}
	p.set(x0, y0+5, lt.ahu, p.bg(x0, y0+5), "▟")
	p.set(x1-1, y0+5, lt.ahu, p.bg(x1-1, y0+5), "▙")
	name, fg := a.name, scene.Mix(lt.ahuText, lt.ahu, .25)
	if a.selected {
		name, fg = "▾ "+a.name, lt.label
	}
	if n := utf8.RuneCountInString(name); name != "" {
		room := a.w - 2
		p.text(x0+1+max(0, (room-n)/2), y0+5, room, name, fg, &lt.ahu)
	}
	for k := range a.slots {
		p.slot(&a.slots[k], y0)
	}
}

// rough is the color of an uncarved stone pixel: lit and shadowed chips at random, the same every frame.
func (p *painter) rough(seed uint64, i int) rgb {
	v := scene.Unit(seed^roughSeed, i)
	switch {
	case v < .3:
		return p.lt.stoneShade
	case v < .75:
		return p.lt.stone
	}
	return scene.Mix(p.lt.stone, p.lt.stoneLit, .6)
}

// stand draws the moai with its left edge at mx: carved from the top down as it progresses, rough below; each
// row of cells shifts right by its lean.
func (p *painter) stand(q *slot, mx, y0 int, lean [3]int) {
	lt := p.lt
	carved := 6
	switch {
	case q.weather == scene.Summit:
	case q.progress < 1.0/3:
		carved = 0
	case q.progress < 2.0/3:
		carved = 4
	}
	seed := scene.SeedOf(q.id)
	pixel := func(row, col int) (rgb, bool) {
		if row >= carved {
			if row == 0 && (col == 0 || col == 4) && carved == 0 {
				return rgb{}, false
			}
			return p.rough(seed, row*5+col), true
		}
		switch figure[row][col] {
		case 'L':
			return lt.stoneLit, true
		case 'S':
			return lt.stone, true
		case 'D':
			return lt.stoneShade, true
		case 'E':
			if q.weather == scene.Summit {
				return lt.coral, true
			}
			return lt.stoneShade, true
		}
		return rgb{}, false
	}
	for r := range 3 {
		for col := range 5 {
			top, tOn := pixel(r*2, col)
			bot, bOn := pixel(r*2+1, col)
			p.px(mx+col+lean[r], y0+2+r, top, bot, tOn, bOn)
		}
	}
	if q.weather == scene.Summit {
		for col := 1; col < 4; col++ {
			p.set(mx+col+lean[0], y0+1, lt.scoria, p.bg(mx+col+lean[0], y0+1), topknot)
		}
	}
}

// toppled draws the moai fallen face down along row y, head first toward the left.
func (p *painter) toppled(mx, y int) {
	lt := p.lt
	for col := range 7 {
		top, bot := lt.stoneLit, lt.stone
		if col == 0 || col == 6 {
			p.px(mx+col, y, top, bot, false, true)
			continue
		}
		if col == 1 {
			bot = lt.stoneShade
		}
		p.px(mx+col, y, top, bot, true, true)
	}
}

func (p *painter) free(x, x1 int) bool { return x >= 0 && x < x1 && x < p.w && !p.occ[x] }

// spot is the free cell nearest want in x0..x1, or -1.
func (p *painter) spot(want, x0, x1 int) int {
	for d := 0; d <= x1-x0; d++ {
		for _, x := range [2]int{want - d, want + d} {
			if x >= x0 && p.free(x, x1) {
				return x
			}
		}
	}
	return -1
}

// slot draws a moai's place on the ahu: weather, the moai, its carvers, its stones, alerts and its name.
func (p *painter) slot(q *slot, y0 int) {
	lt := p.lt
	x0, x1 := q.x, q.x+q.w
	mx := x0 + 2
	if q.dying > 0 {
		p.kill(q, mx, y0)
		return
	}
	switch q.weather {
	case scene.Clouds:
		seed := scene.SeedOf(q.id)
		for x := x0; x < x1; x++ {
			v := scene.Noise(seed, float64(x)*.4+float64(p.frame/10)*.3)
			g := "░"
			if v > .6 {
				g = "▒"
			}
			p.set(x, y0+1, lt.mist, p.bg(x, y0+1), g)
			if v > .7 {
				p.set(x, y0+6, lt.mist, p.bg(x, y0+6), "░")
			}
		}
	case scene.Storm:
		for x := x0; x < x1; x++ {
			if (x+p.frame)%3 == 0 {
				p.air(x, y0+1, lt.rain, "╱")
			}
			switch v := scene.Spread(waveSeed, x*7+p.frame/2) % 7; {
			case v < 2:
				p.set(x, y0+7, scene.White, scene.Mix(lt.seaFar, lt.foam, .5), "^")
			case v < 4:
				p.set(x, y0+7, lt.foam, scene.Mix(lt.seaFar, lt.seaNear, .4), "*")
			case v == 4:
				p.air(x, y0+6, lt.foam, "'")
			}
		}
	}
	if q.weather == scene.Eruption {
		p.toppled(mx-1, y0+4)
		for x := mx - 1; x < mx+6; x++ {
			if (x*5+p.frame/2)%3 != 0 {
				p.set(x, y0+3, lt.dust, p.bg(x, y0+3), "░")
			}
		}
	} else {
		p.stand(q, mx, y0, [3]int{})
	}
	p.carvers(q, mx, y0)
	p.stones(q, y0)
	if q.needs {
		g := fireA
		if p.frame/4%2 == 1 {
			g = fireB
		}
		p.signal(x0, y0+1, lt.red, g)
		p.signal(x0, y0+2, lt.red, fireBase)
	}
	p.label(q, y0)
}

// carvers stands the moai's agents on the ahu beside it: red carvers first and still, busy ones swinging their
// adzes, idle ones sitting.
func (p *painter) carvers(q *slot, mx, y0 int) {
	lt := p.lt
	x0, x1 := q.x, q.x+q.w
	body0, body1 := mx, mx+5
	if q.weather == scene.Eruption {
		body0, body1 = mx-1, mx+6
	}
	for x := x0; x < x1 && x < p.w; x++ {
		p.occ[x] = x >= body0 && x < body1
	}
	if q.needs {
		p.occ[x0] = true
	}
	y := y0 + 4
	for _, a := range q.team {
		if a.Stance == scene.Waiting {
			if x := p.spot(mx-1, x0, x1); x >= 0 {
				p.signal(x, y, lt.red, busyA)
				p.occ[x] = true
			}
		}
	}
	if q.weather == scene.Eruption {
		return
	}
	k := 0
	for _, a := range q.team {
		if a.Stance != scene.Climbing {
			continue
		}
		if x := p.spot(mx+5, x0, x1); x >= 0 {
			g := busyA
			if (p.frame/4+k)%2 == 1 {
				g = busyB
				if x == mx+5 || x == mx-1 {
					p.air(x, y-1, lt.stoneLit, "'")
				}
			}
			p.signal(x, y, lt.carver, g)
			p.occ[x] = true
		}
		k++
	}
	for _, a := range q.team {
		if a.Stance != scene.Resting {
			continue
		}
		if x := p.spot(x1-1, x0, x1); x >= 0 {
			p.air(x, y, lt.resting, rester)
			p.occ[x] = true
		}
	}
}

func merged(c scene.Camp) bool { return c.State == scene.Merged || c.Stage >= 6 }

// stones lays the moai's pull requests on the grass at its foot, further right the more gates they passed, merged
// ones as pukao topknots at the right end; "+N" counts stones with no room.
func (p *painter) stones(q *slot, y0 int) {
	lt := p.lt
	x0, x1 := q.x, q.x+q.w
	y := y0 + 6
	for x := x0; x < x1 && x < p.w; x++ {
		p.occ[x] = false
	}
	span := max(1, q.w-3)
	hidden := 0
	for stage := 6; stage >= 0; stage-- {
		for _, c := range q.camps {
			at := min(max(c.Stage, 0), 5)
			if merged(c) {
				at = 6
			}
			if at != stage {
				continue
			}
			x := p.spot(x0+int(math.Round(float64(stage)/6*float64(span))), x0, x1-1)
			if x < 0 {
				hidden++
				continue
			}
			switch {
			case merged(c):
				p.air(x, y, lt.scoria, pukao)
			case c.State == scene.Failing:
				p.signal(x, y, lt.red, cracked)
			case c.State == scene.Green:
				p.air(x, y, lt.green, stone)
			default:
				p.air(x, y, lt.rock, stone)
			}
			p.occ[x] = true
		}
	}
	if hidden > 0 {
		s := p.count('+', hidden)
		p.text(x1-len(s), y, len(s), s, lt.rock, nil)
	}
}

// count is a sign and n, written into a buffer the painter reuses.
func (p *painter) count(sign byte, n int) string {
	p.textBuf = strconv.AppendInt(append(p.textBuf[:0], sign), int64(n), 10)
	return string(p.textBuf)
}

// label names a moai with how far along it is; red while it needs the owner, lit and marked when selected.
func (p *painter) label(q *slot, y0 int) {
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
		name = strconv.Itoa(q.units) + " m"
	}
	if n := utf8.RuneCountInString(name); n+len(pct)+2 > room {
		name = string([]rune(name)[:max(0, min(n, room-len(pct)-3))]) + "…"
	}
	p.textBuf = append(append(append(append(p.textBuf[:0], name...), ' '), pct...), '%')
	if q.needs || q.dying > 0 {
		p.recolor(x, y0, room, string(p.textBuf), fg)
		return
	}
	p.text(x, y0, room, string(p.textBuf), fg, nil)
}

// recolor writes text in fg as it is, red staying red and a fading name fading, over the backdrop darkened
// until a red name reads.
func (p *painter) recolor(x, y, n int, s string, fg rgb) {
	k := 0
	for _, r := range s {
		if k >= n {
			break
		}
		bg := p.bg(x+k, y)
		for f := .75; scene.IsRed(fg) && scene.Contrast(fg, bg) < scene.MinContrast && f > .1; f *= .75 {
			bg = bg.Scale(f)
		}
		p.set(x+k, y, fg, bg, scene.Glyph(r))
		k++
	}
}

// kill topples the moai forward off its ahu into the cove below: it leans, falls flat past the platform's edge
// and goes under in a splash that spreads and fades.
func (p *painter) kill(q *slot, mx, y0 int) {
	lt, d := p.lt, q.dying
	switch {
	case d < .3:
		t := d / .3
		p.stand(q, mx, y0, [3]int{int(t * 3), int(t * 1.5), 0})
	case d < .5:
		p.toppled(mx+1, y0+5)
	case d < .6:
		p.toppled(mx+1, y0+6)
	default:
		s := (d - .6) / .4
		cx := mx + 4
		reach := 1 + int(s*4)
		for x := cx - reach; x <= cx+reach; x++ {
			p.set(x, y0+7, scene.Mix(lt.foam, lt.seaFar, s*.7), scene.Mix(lt.seaFar, lt.seaNear, .4), "≈")
		}
		if s < .6 {
			p.air(cx-1, y0+6, lt.foam, "'")
			p.air(cx, y0+6, lt.foam, "*")
			p.air(cx+1, y0+6, lt.foam, "'")
		}
		if s < .3 {
			p.air(cx, y0+4, lt.foam, "·")
			p.air(cx-2, y0+5, lt.foam, "'")
			p.air(cx+2, y0+5, lt.foam, "'")
		}
	}
	p.label(q, y0)
}

// bracket draws a ceremonial site's line under the ahu it holds, named for its chief.
func (p *painter) bracket(f bracket) {
	lt, y := p.lt, p.l.row(f.band)+8
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
	name := " " + f.name + " site "
	if f.selected {
		name, fg = " ▾ "+f.name+" site ", lt.label
	}
	n, room := utf8.RuneCountInString(name), f.x1-f.x0-4
	if n > room && room > 2 {
		name, n = string([]rune(name)[:room-2])+"… ", room
	}
	p.text(f.x0+2+max(0, (room-n)/2), y, room, name, fg, nil)
}
