package stocks

import (
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type painter struct {
	c     *scene.Canvas
	box   scene.Rect
	pal   palette
	frame int
	plain bool
	sel   string
	w, h  int
	*scratch
}

// scratch is the per-frame buffers, pooled so frames do not allocate them.
type scratch struct {
	vals, sum []float64
	num       []byte
}

var scratches = sync.Pool{New: func() any { return &scratch{} }}

func (p *painter) paint(s scene.Scene, b board) {
	p.scratch = scratches.Get().(*scratch)
	defer scratches.Put(p.scratch)
	for y := range p.h {
		p.fill(0, p.w, y, p.pal.bg)
	}
	p.topBar(s)
	for _, bl := range b.blocks {
		switch {
		case bl.kind == head:
			p.header(bl, b.rows)
		case bl.kind == index:
			p.indexChart(bl)
		case b.rows == 1:
			p.quote(bl)
		default:
			p.ticker(bl, b.rows)
		}
	}
	p.tape(s)
}

func (p *painter) put(x, y int, fg, bg rgb, g string) {
	if p.box.Has(x, y) {
		return
	}
	if p.plain {
		p.c.Plain(x, y, g)
		return
	}
	p.c.Set(x, y, fg, bg, g)
}

func (p *painter) fill(x0, x1, y int, bg rgb) {
	for x := x0; x < x1; x++ {
		p.put(x, y, bg, bg, " ")
	}
}

// text writes s from x, stopping before end, and is where it stopped; the first struck runes are struck through.
func (p *painter) text(x, y, end int, s string, fg, bg rgb, struck int) int {
	for _, r := range s {
		if x >= end {
			break
		}
		g := scene.Glyph(r)
		if struck > 0 && r != ' ' {
			g = strike(r)
		}
		struck--
		p.put(x, y, fg, bg, g)
		x++
	}
	return x
}

// price writes v as a price ending just before end and is where it starts.
func (p *painter) price(end, y int, v float64, fg, bg rgb) int {
	p.num = strconv.AppendFloat(p.num[:0], v, 'f', 2, 64)
	x := end - len(p.num)
	for i, ch := range p.num {
		p.put(x+i, y, fg, bg, scene.Glyph(rune(ch)))
	}
	return x
}

func (p *painter) flash() bool { return p.frame/4%2 == 0 }

func (p *painter) values(n int) []float64 {
	if cap(p.vals) < n {
		p.vals = make([]float64, n)
	}
	return p.vals[:n]
}

// badge writes how a ticker or panel trades, ending just before end, and is where it starts.
func (p *painter) badge(end, y int, w scene.Weather, dying float64, rowBg rgb, short bool) int {
	label, fg, bg := "", p.pal.text, rowBg
	switch {
	case dying > 0:
		label, fg = "DELISTED", p.pal.dim
		if short {
			label = "DLST"
		}
	case w == scene.Clouds:
		label, fg, bg = "FLAT", p.pal.dim, p.pal.haze
	case w == scene.Storm:
		label, fg, bg = "CRASH", p.pal.selText, p.pal.violet
	case w == scene.Eruption:
		label, fg, bg = "HALTED", p.pal.haltText, p.pal.halt
		if short {
			label = "HALT"
		}
	case w == scene.Summit:
		label, fg = "ATH✦", p.pal.gold
	default:
		return end
	}
	x := end - len([]rune(label)) - 2
	p.put(x, y, fg, bg, " ")
	p.text(x+1, y, end, label, fg, bg, 0)
	p.put(end-1, y, fg, bg, " ")
	return x
}

func (p *painter) header(bl block, rows int) {
	pn := bl.pn
	y, x0, x1 := bl.r.y0, bl.r.x0, bl.r.x1-1
	bg, fg, kfg := p.pal.head, p.pal.headText, p.pal.kind
	on := pn.id != "" && pn.id == p.sel
	if on {
		bg, fg, kfg = p.pal.sel, p.pal.selText, p.pal.selText
	}
	p.fill(x0, x1, y, bg)
	if on {
		p.put(x0, y, fg, bg, "▸")
	}
	end := p.badge(x1, y, pn.sky, 0, bg, x1-x0 < chartCol)
	if pn.index {
		avg := 0.0
		for _, m := range pn.members {
			avg += scene.Clamp(m.Progress, 0, 1)
		}
		if len(pn.members) > 0 {
			avg /= float64(len(pn.members))
		}
		end = p.price(end-1, y, avg*100, fg, bg) - 1
	}
	x := p.text(x0+1, y, end, pn.kind, kfg, bg, 0)
	x = p.text(x+1, y, end, pn.title, fg, bg, 0)
	if bl.cont {
		x = p.text(x, y, end, " (CONT)", kfg, bg, 0)
	}
	if pn.hidden > 0 {
		x = p.text(x, y, end, " +", kfg, bg, 0)
		x = p.text(x, y, end, strconv.Itoa(pn.hidden), kfg, bg, 0)
	}
	if pn.index && rows == 1 && end-x > 6 {
		vals := p.indexWalk(pn, end-x-2)
		p.spark(x+1, y, vals, len(vals), len(vals), len(vals), fg, fg, p.pal.dim, bg)
	}
}

// indexWalk is the average of the index's goals' price lines, n columns long.
func (p *painter) indexWalk(pn *panel, n int) []float64 {
	if cap(p.sum) < n {
		p.sum = make([]float64, n)
	}
	sum := p.sum[:n]
	clear(sum)
	vals := p.values(n)
	for _, m := range pn.members {
		walk(scene.SeedOf(m.ID), vals, m.Progress, m.Weather)
		for i, v := range vals {
			sum[i] += v
		}
	}
	for i := range sum {
		sum[i] /= float64(max(1, len(pn.members)))
	}
	return sum
}

func (p *painter) indexChart(bl block) {
	x0, x1 := bl.r.x0+1, bl.r.x1-1
	rows := bl.r.y1 - bl.r.y0
	for y := bl.r.y0; y < bl.r.y1; y++ {
		p.fill(bl.r.x0, x1, y, p.pal.panel)
	}
	if x1 <= x0 {
		return
	}
	vals := p.indexWalk(bl.pn, x1-x0)
	n := len(vals)
	p.chart(x0, bl.r.y0, rows, vals, ink{line: p.pal.idx, fill: scene.Mix(p.pal.idx, p.pal.panel, .8), bg: p.pal.panel, drawn: n, altFrom: n, flatFrom: n})
}

// ink is how a chart is drawn: its line and fill, the columns past altFrom in alt, past flatFrom flat at zero,
// and nothing past drawn.
type ink struct {
	line, fill, bg, grid, alt, altFill, flat rgb
	drawn, altFrom, flatFrom                 int
}

// chart draws vals as a filled line rows tall from x, y, two pixels a cell.
func (p *painter) chart(x, y, rows int, vals []float64, k ink) {
	px := 2 * rows
	prev := -1
	for i, v := range vals {
		if i >= k.drawn {
			break
		}
		line, fill := k.line, k.fill
		if i >= k.altFrom {
			line, fill = k.alt, k.altFill
		}
		if i >= k.flatFrom {
			v, line, fill = 0, k.flat, k.bg
		}
		hp := int(math.Round(v * float64(px-1)))
		lo, hi := hp, hp
		if prev >= 0 {
			lo, hi = min(hp, prev), max(hp, prev)
		}
		prev = hp
		color := func(py int) (rgb, bool) {
			switch {
			case py >= lo && py <= hi:
				return line, true
			case py < lo:
				return scene.Mix(fill, k.bg, float64(lo-py)/float64(px)), false
			case py == px/2 && (x+i)%4 == 0 && k.grid != (rgb{}):
				return k.grid, false
			}
			return k.bg, false
		}
		for r := range rows {
			up, upLine := color(2*(rows-1-r) + 1)
			dn, dnLine := color(2 * (rows - 1 - r))
			if p.plain {
				p.put(x+i, y+r, up, dn, [4]string{" ", "▄", "▀", "█"}[b2i(upLine)*2+b2i(dnLine)])
				continue
			}
			p.put(x+i, y+r, up, dn, "▀")
		}
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// cellRow is the row, counted from the chart's top, holding the top of the line at v.
func cellRow(v float64, rows int) int {
	hp := int(math.Round(v * float64(2*rows-1)))
	return rows - 1 - hp/2
}

var levels = [...]string{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// spark draws vals as a one-row sparkline from x: flat at zero from flatFrom, alt from altFrom, blank from drawn.
func (p *painter) spark(x, y int, vals []float64, drawn, altFrom, flatFrom int, line, alt, flat, bg rgb) {
	for i, v := range vals {
		switch {
		case i >= flatFrom:
			p.put(x+i, y, flat, bg, "▁")
		case i >= drawn:
			p.put(x+i, y, p.pal.halt, bg, "╌")
		case i >= altFrom:
			p.put(x+i, y, alt, bg, levels[int(math.Round(v*7))])
		default:
			p.put(x+i, y, line, bg, levels[int(math.Round(v*7))])
		}
	}
}

// unitInk is how a ticker's colors are, faded toward the panel as it is delisted.
type unitInk struct {
	pal  palette
	fade float64
}

func (u unitInk) of(c rgb) rgb { return scene.Mix(c, u.pal.panel, u.fade) }

func (p *painter) lineInk(u scene.Peak, n int, fade unitInk) ink {
	k := ink{line: fade.of(p.pal.line), fill: fade.of(p.pal.fill), bg: p.pal.panel, grid: p.pal.grid, flat: fade.of(p.pal.dim), drawn: n, altFrom: n, flatFrom: n}
	switch u.Weather {
	case scene.Clouds:
		k.line, k.fill = fade.of(p.pal.dim), fade.of(p.pal.haze)
	case scene.Storm:
		k.altFrom = int(crashKnee * float64(n))
		k.alt, k.altFill = fade.of(p.pal.violet), fade.of(scene.Mix(p.pal.violet, p.pal.panel, .5))
		if p.frame/2%3 == 0 {
			k.altFill = fade.of(p.pal.violet)
		}
	case scene.Eruption:
		k.drawn = max(1, int(math.Round(haltKnee*float64(n))))
	}
	if d := scene.Clamp(u.Dying, 0, 1); d > 0 {
		k.flatFrom = int((1 - d) * float64(n))
	}
	return k
}

// row is the colors of a ticker's top row: lit when selected, red while its needs-you alert flashes.
func (p *painter) row(u scene.Peak) (bg, fg, sym, name rgb) {
	bg, fg, sym, name = p.pal.panel, p.pal.text, p.pal.sym, p.pal.dim
	if u.ID == p.sel {
		bg, fg, sym, name = p.pal.sel, p.pal.selText, p.pal.selText, p.pal.selText
	}
	if u.NeedsYou && u.Dying == 0 && p.flash() {
		bg, fg, sym, name = p.pal.red, p.pal.redText, p.pal.redText, p.pal.redText
	}
	return
}

func struckOf(d float64, n int) int { return int(math.Ceil(scene.Clamp(d, 0, 1) * float64(n))) }

func (p *painter) ticker(bl block, rows int) {
	u := bl.pn.units[bl.unit]
	seed := scene.SeedOf(u.ID)
	d := scene.Clamp(u.Dying, 0, 1)
	fade := unitInk{p.pal, scene.Smooth(.55, 1, d) * .85}
	alive := d == 0
	x0, x1, y0 := bl.r.x0, bl.r.x1-1, bl.r.y0
	chartRows := rows - 2
	for y := y0; y < y0+rows; y++ {
		p.fill(x0, x1, y, p.pal.panel)
	}
	nc := min(len(u.Camps), max(0, (x1-x0-24)/2))
	cx := x1
	if nc > 0 {
		cx = x1 - 2*nc - 1
	}
	n := cx - (x0 + 1)
	vals := p.values(n)
	walk(seed, vals, u.Progress, u.Weather)
	k := p.lineInk(u, n, fade)
	p.chart(x0+1, y0+1, chartRows, vals, k)
	if u.Weather == scene.Eruption && alive {
		p.halt(x0+1, cx, y0+1+chartRows/2)
	}
	if u.Weather == scene.Summit && alive && n > 0 {
		p.fireworks(x0+1, y0+1, chartRows, vals, seed)
	}

	bg, fg, symFg, nameFg := p.row(u)
	p.fill(x0, x1, y0, fade.of(bg))
	bg = fade.of(bg)
	end := p.badge(cx, y0, u.Weather, d, bg, false)
	if n > 6 {
		ch := (vals[n-1] - vals[n-1-max(1, n/6)]) * 100
		arrow := "▲"
		if ch < 0 {
			arrow = "▼"
		}
		end = p.price(end-1, y0, math.Abs(ch), fade.of(nameFg), bg)
		end--
		p.put(end, y0, fade.of(nameFg), bg, arrow)
	}
	end = p.price(end-1, y0, scene.Clamp(u.Progress, 0, 1)*100, fade.of(fg), bg) - 1
	if u.ID == p.sel {
		p.put(x0, y0, fg, bg, "▸")
		for y := y0 + 1; y < y0+rows; y++ {
			p.put(x0, y, p.pal.sel, p.pal.panel, "▌")
		}
	}
	sym := symbol(u.Name)
	struck := struckOf(d, len([]rune(sym))+len([]rune(u.Name))+1)
	x := p.text(x0+1, y0, end, sym, fade.of(symFg), bg, struck)
	struck -= len([]rune(sym)) + 1
	if alive && u.NeedsYou {
		x = p.text(x+1, y0, end, " !! NEEDS YOU ", p.pal.redText, p.pal.red, 0)
	}
	p.text(x+1, y0, end, u.Name, fade.of(nameFg), bg, struck)

	for i := range nc {
		p.candle(cx+1+2*i, y0, chartRows, u.Camps[i], fade, bg)
	}
	if nc > 0 {
		for y := y0 + 1; y < y0+1+chartRows; y++ {
			p.put(cx, y, p.pal.grid, p.pal.panel, "│")
		}
	}
	if alive {
		p.volume(x0+1, cx, y0+rows-1, u, seed)
	}
}

// candle is a pull request: a body as tall as the gates it passed, colored by its CI, with a dividend flag on
// the top row once merged.
func (p *painter) candle(x, y0, rows int, c scene.Camp, fade unitInk, rowBg rgb) {
	body, glyph := p.pal.open, "│"
	switch c.State {
	case scene.Green:
		body, glyph = p.pal.green, "█"
	case scene.Failing:
		body, glyph = p.pal.red, "▒"
	case scene.Merged:
		body, glyph = p.pal.gold, "█"
		p.put(x, y0, fade.of(p.pal.gold), rowBg, "⚑")
	}
	body = fade.of(body)
	px := 2 * rows
	top := int(math.Round(float64(min(max(c.Stage, 0), 6)) / 6 * float64(px-1)))
	color := func(py int) rgb {
		switch {
		case py <= top:
			return body
		case py == top+1 && c.State != scene.Merged:
			return fade.of(p.pal.dim)
		}
		return p.pal.panel
	}
	for r := range rows {
		up, dn := 2*(rows-1-r)+1, 2*(rows-1-r)
		if p.plain {
			switch {
			case up <= top:
				p.put(x, y0+1+r, body, body, glyph)
			case dn <= top:
				p.put(x, y0+1+r, body, body, "▄")
			}
			continue
		}
		p.put(x, y0+1+r, color(up), color(dn), "▀")
	}
}

// volume is the agents along the bottom row: their names from the left and a bar each by the latest price,
// pulsing while busy, a red alert tick while waiting and flat while resting.
func (p *painter) volume(x0, x1, y int, u scene.Peak, seed uint64) {
	nb := min(len(u.Team), max(0, (x1-x0-4)/3))
	bx := x1 - 3*nb
	for i, a := range u.Team[:nb] {
		x := bx + 3*i
		switch a.Stance {
		case scene.Climbing:
			g := levels[2+int(scene.Unit(seed, p.frame/4*7+i)*6)]
			p.put(x, y, p.pal.busy, p.pal.panel, g)
			p.put(x+1, y, p.pal.busy, p.pal.panel, g)
		case scene.Waiting:
			p.put(x, y, p.pal.redText, p.pal.red, "!")
			p.put(x+1, y, p.pal.redText, p.pal.red, "!")
		default:
			p.put(x, y, p.pal.rest, p.pal.panel, "▁")
			p.put(x+1, y, p.pal.rest, p.pal.panel, "▁")
		}
	}
	x := x0
	for _, a := range u.Team {
		if x >= bx-1 {
			break
		}
		c := p.pal.busy
		switch a.Stance {
		case scene.Waiting:
			c = p.pal.red
		case scene.Resting:
			c = p.pal.dim
		}
		x = p.text(x, y, bx-1, a.Name, c, p.pal.panel, 0) + 2
	}
}

// halt is the trading-halted banner: hazard stripes across the chart with the notice in the middle.
func (p *painter) halt(x0, x1, y int) {
	for x := x0; x < x1; x++ {
		g := "▚"
		if (x+p.frame)%2 == 0 {
			g = "▞"
		}
		p.put(x, y, p.pal.halt, p.pal.haltText, g)
	}
	label := " TRADING HALTED "
	if x1-x0 < 24 {
		label = " HALTED "
	}
	p.text(x0+(x1-x0-len(label))/2, y, x1, label, p.pal.haltText, p.pal.halt, 0)
}

var sparks = [...]string{"·", "*", "+", "·"}

// fireworks is the all-time-high star at the line's end, with sparks bursting in the clear air around it.
func (p *painter) fireworks(x, y, rows int, vals []float64, seed uint64) {
	n := len(vals)
	star := "✦"
	if p.frame/4%2 == 1 {
		star = "✧"
	}
	p.put(x+n-1, y+cellRow(vals[n-1], rows), p.pal.gold, p.pal.panel, star)
	for k := range 4 {
		dx := 1 + int(scene.Unit(seed, p.frame/2*4+k)*6)
		col := n - 1 - dx
		if col < 0 {
			continue
		}
		r := int(scene.Unit(seed, p.frame/2*4+k+100) * float64(cellRow(vals[col], rows)))
		if r >= cellRow(vals[col], rows) {
			continue
		}
		p.put(x+col, y+r, scene.Mix(p.pal.gold, p.pal.text, float64(k)/4), p.pal.panel, sparks[k])
	}
}

func (p *painter) quote(bl block) {
	u := bl.pn.units[bl.unit]
	seed := scene.SeedOf(u.ID)
	d := scene.Clamp(u.Dying, 0, 1)
	fade := unitInk{p.pal, scene.Smooth(.55, 1, d) * .85}
	alive := d == 0
	y, x0, x1 := bl.r.y0, bl.r.x0, bl.r.x1-1
	bg, fg, symFg, _ := p.row(u)
	bg = fade.of(bg)
	p.fill(x0, x1, y, bg)
	if u.ID == p.sel {
		p.put(x0, y, fg, bg, "▸")
	}
	sym := symbol(u.Name)
	symBg := bg
	if alive && u.NeedsYou {
		symFg, symBg = p.pal.redText, p.pal.red
	}
	p.text(x0+1, y, x0+7, sym, fade.of(symFg), symBg, struckOf(d, len([]rune(sym))))
	p.price(x0+13, y, scene.Clamp(u.Progress, 0, 1)*100, fade.of(fg), bg)
	end := p.badge(x1, y, u.Weather, d, bg, true)
	nc := min(len(u.Camps), max(0, (end-x0-20)/2))
	end -= nc + 1
	for i, c := range u.Camps[:nc] {
		col, g := p.pal.open, levels[min(max(c.Stage, 0), 6)]
		switch c.State {
		case scene.Green:
			col = p.pal.green
		case scene.Failing:
			col = p.pal.red
		case scene.Merged:
			col, g = p.pal.gold, "⚑"
		}
		p.put(end+1+i, y, fade.of(col), bg, g)
	}
	if alive {
		na := min(len(u.Team), max(0, (end-x0-18)/2))
		end -= na + 1
		for i, a := range u.Team[:na] {
			switch a.Stance {
			case scene.Climbing:
				p.put(end+1+i, y, p.pal.busy, bg, levels[2+int(scene.Unit(seed, p.frame/4*7+i)*6)])
			case scene.Waiting:
				p.put(end+1+i, y, p.pal.redText, p.pal.red, "!")
			default:
				p.put(end+1+i, y, p.pal.rest, bg, "▁")
			}
		}
	}
	n := end - (x0 + 14)
	if n < 3 {
		return
	}
	vals := p.values(n)
	walk(seed, vals, u.Progress, u.Weather)
	k := p.lineInk(u, n, fade)
	line, alt := k.line, k.alt
	if u.ID == p.sel || bg != fade.of(p.pal.panel) {
		line, alt = fade.of(fg), fade.of(fg)
	}
	p.spark(x0+14, y, vals, k.drawn, k.altFrom, k.flatFrom, line, alt, k.flat, bg)
	if u.Weather == scene.Summit && alive {
		p.put(x0+14+n-1, y, p.pal.gold, bg, "✦")
	}
}

func (p *painter) topBar(s scene.Scene) {
	bg, fg := p.pal.bar, p.pal.barText
	p.fill(0, p.w, 0, bg)
	sectors, tickers := 0, 0
	s.Walk(func(scene.Peak) { tickers++ }, func(scene.Range) { sectors++ })
	x := p.text(1, 0, p.w, "YM <GO>  EQUITY MONITOR  ", fg, bg, 0)
	x = p.text(x, 0, p.w, strconv.Itoa(sectors), fg, bg, 0)
	x = p.text(x, 0, p.w, " SECTORS  ", fg, bg, 0)
	x = p.text(x, 0, p.w, strconv.Itoa(tickers), fg, bg, 0)
	p.text(x, 0, p.w, " TICKERS", fg, bg, 0)
	session, clock := market(s.Now)
	right := session + "  " + clock + " "
	p.text(p.w-len(right), 0, p.w, right, fg, bg, 0)
}

// market is the trading session at a local time and the clock; the zero time is dusk, after hours.
func market(now time.Time) (string, string) {
	if now.IsZero() {
		return "AFTER HOURS", "--:--"
	}
	hour := float64(now.Hour()) + float64(now.Minute())/60
	clock := now.Format("15:04")
	switch {
	case now.Weekday() == time.Saturday || now.Weekday() == time.Sunday:
		return "MARKET CLOSED", clock
	case hour >= 4 && hour < 9.5:
		return "PRE-MARKET", clock
	case hour >= 9.5 && hour < 16:
		return "MARKET OPEN", clock
	case hour >= 16 && hour < 20:
		return "AFTER HOURS", clock
	}
	return "MARKET CLOSED", clock
}

// tape is the ticker tape crawling along the bottom row: each unit's symbol and price.
func (p *painter) tape(s scene.Scene) {
	y := p.h - 1
	if y <= 0 {
		return
	}
	var runes []rune
	var syms []bool
	s.Walk(func(u scene.Peak) {
		for _, r := range symbol(u.Name) {
			runes, syms = append(runes, r), append(syms, true)
		}
		p.num = strconv.AppendFloat(append(p.num[:0], ' '), scene.Clamp(u.Progress, 0, 1)*100, 'f', 2, 64)
		p.num = append(p.num, "   "...)
		for _, ch := range p.num {
			runes, syms = append(runes, rune(ch)), append(syms, false)
		}
	}, func(scene.Range) {})
	if len(runes) == 0 {
		return
	}
	off := p.frame / 4
	for x := range p.w {
		i := (x + off) % len(runes)
		fg := p.pal.dim
		if syms[i] {
			fg = p.pal.sym
		}
		p.put(x, y, fg, p.pal.bg, scene.Glyph(runes[i]))
	}
}
