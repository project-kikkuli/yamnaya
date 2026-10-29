package kaiju

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sprites draws the work over the print, the most important first so nothing later takes its cells: names and
// alerts, defenders waiting on the owner, installations, jets at work, parked crews, district names, then rain.
func (p *painter) sprites() {
	defer func() { p.writer = 0 }()
	order := p.bufs.order[:0]
	for i := range p.l.monsters {
		order = append(order, i)
	}
	p.bufs.order = order
	slices.SortStableFunc(order, func(a, b int) int {
		ma, mb := &p.l.monsters[a], &p.l.monsters[b]
		return cmp.Or(-cmp.Compare(boolf(ma.selected), boolf(mb.selected)), -cmp.Compare(boolf(ma.needsYou), boolf(mb.needsYou)))
	})
	for pass := range 5 {
		for _, i := range order {
			p.writer = int32(i + 1)
			switch pass {
			case 0:
				p.crown(i)
			case 1:
				p.waiting(i)
			case 2:
				p.camps(i)
			case 3:
				p.jets(i)
			default:
				p.parked(i)
			}
		}
	}
	p.writer = 0
	for g := range p.l.districts {
		p.district(g)
	}
	p.eggWords()
	for i := range p.l.monsters {
		if m := &p.l.monsters[i]; m.weather == scene.Storm && m.dying == 0 {
			p.rain(m)
		}
	}
}

func boolf(b bool) int {
	if b {
		return 1
	}
	return 0
}

func row(y float64) int { return int(math.Floor(y / 2)) }

// crown names a monster over its head with its done percentage; a blinking red air-raid siren beside the name
// when it waits on the owner, the selection's marker above, and the end card over a monster on its tower.
func (p *painter) crown(i int) {
	m := &p.l.monsters[i]
	if m.dying > .5 {
		return
	}
	name := m.name
	if name == "" {
		name = strconv.Itoa(m.leaves) + " monsters"
	}
	pct := ""
	if len(m.team)+len(m.camps) > 0 {
		pct = " " + strconv.Itoa(int(math.Round(m.progress*100))) + "%"
	}
	texts := []string{name + pct, name}
	if short, ok := scene.Short(name, p.l.unitNames, max(scene.ShortMin+1, int(m.span())+2)); ok && short != name {
		texts = append(texts, short+pct, short)
	}
	color := p.pal.label
	switch {
	case m.needsYou && m.dying == 0:
		color = p.pal.flare
	case m.selected:
		color = p.pal.jet
	}
	head := row(m.top()) - 1
	cx := int(m.x)
	lx, ly, placed := 0, 0, false
	for _, text := range texts {
		n := ansi.StringWidth(text)
		var at [][2]int
		for _, r := range []int{head, head - 1, head + 1, head - 2} {
			r = max(r, row(p.l.top))
			at = append(at, [2]int{cx - n/2, r}, [2]int{cx - n/2 + n/3 + 1, r}, [2]int{cx - n/2 - n/3 - 1, r})
		}
		if lx, ly, placed = p.label(text, color, at...); placed {
			break
		}
	}
	if !placed {
		lx, ly = cx, head
	}
	if m.dying > 0 {
		return
	}
	if m.needsYou {
		siren := "◉"
		if p.frame/2%2 == 1 {
			siren = "◎"
		}
		_ = p.mark(lx-2, ly, siren, p.pal.flare) || p.mark(cx, ly-1, siren, p.pal.flare) || p.mark(cx, ly+1, siren, p.pal.flare)
	}
	if m.selected {
		_ = p.mark(cx, ly-1, "▾", p.pal.jet) || p.mark(cx, ly-2, "▾", p.pal.jet)
	}
	if m.weather == scene.Summit {
		_ = p.wide(cx+2, ly-1, "終", p.pal.gold) || p.wide(cx-4, ly-1, "終", p.pal.gold) || p.wide(cx-1, ly-2, "終", p.pal.gold)
	}
}

// line is the cell row of the quay the defenders hold.
func (p *painter) line() int { return row(p.l.ground) + 1 }

// waiting halts each defender stopped on the owner on the quay below the monster: a red tank under a red flare,
// its crew's name beside it in red.
func (p *painter) waiting(i int) {
	m := &p.l.monsters[i]
	if m.dying > 0 {
		return
	}
	y := p.line()
	j := 0
	for _, c := range m.team {
		if c.Stance != scene.Waiting {
			continue
		}
		for try := range 6 {
			side := 1 - 2*((j+try)%2)
			x := int(m.x) + side*(int(m.span()/2)+2+3*try)
			flare := y - 1 - p.frame/2%2
			if !p.open(x, flare) || !p.mark(x, y, "◆", p.pal.flare) {
				continue
			}
			p.mark(x, flare, "✶", p.pal.flare)
			p.name(x, y, side, c.Name, p.pal.flare)
			break
		}
		j++
	}
}

// name writes an agent's name beside its glyph, on side when there is room.
func (p *painter) name(x, y, side int, name string, fg rgb) {
	name, ok := scene.Short(ansi.Strip(name), p.l.agentNames, 16)
	if !ok {
		return
	}
	n := ansi.StringWidth(name)
	right, left := [2]int{x + 2, y}, [2]int{x - n - 1, y}
	if side < 0 {
		right, left = left, right
	}
	p.label(name, fg, right, left)
}

// camps sets up the monster's pull requests as the army's installations: a barricade on the beach for few gates
// passed, further inland the more; a missile battery once CI is green, burning red when it fails. Merged ones
// are flattened ground the monster has claimed, under one flag.
func (p *painter) camps(i int) {
	m := &p.l.monsters[i]
	if m.dying > 0 {
		return
	}
	merged, last := 0, ""
	j := 0
	for _, c := range m.camps {
		if c.State == scene.Merged {
			merged++
			last = c.Label
			continue
		}
		x, y := p.campAt(m, j, c)
		j++
		glyph, fg := "╥", p.pal.dim
		switch c.State {
		case scene.Green:
			glyph, fg = "▲", p.pal.green
		case scene.Failing:
			glyph, fg = "▲", p.pal.flare
		}
		if p.mark(x, y, glyph, fg) {
			p.tag(x, y, c.Label, fg)
		}
	}
	if merged > 0 {
		if merged > 1 {
			last = strconv.Itoa(merged) + " merged"
		}
		x, y := int(m.x-m.facing*(m.span()/2+3)), row(math.Min(m.foot, p.l.quay+2))
		for _, dy := range []int{0, -1, 1} {
			if p.mark(x, y+dy, "⚑", p.pal.gold) {
				p.tag(x, y+dy, last, p.pal.gold)
				break
			}
		}
	}
}

// campAt is the cell of the monster's j-th standing pull request: on the beach at stage 0, among the buildings
// at stage 6, alternately ahead of and behind it.
func (p *painter) campAt(m *monster, j int, c scene.Camp) (int, int) {
	shore, inland := p.l.quay+3, p.l.ground-(p.l.bottom-p.l.top)*.1
	y := lerp(shore, inland, float64(min(max(c.Stage, 0), 6))/6)
	side := m.facing
	if j%2 == 1 {
		side = -side
	}
	return int(m.x + side*(m.span()/2+4+float64(j/2)*7)), row(y)
}

func (p *painter) tag(x, y int, label string, fg rgb) {
	label = ansi.Truncate(ansi.Strip(label), 10, "…")
	n := ansi.StringWidth(label)
	p.label(label, fg, [2]int{x + 2, y}, [2]int{x - n - 1, y})
}

// fires burns red at each failing installation, licking up out of the cell above it.
func (p *painter) fires(i int) {
	m := &p.l.monsters[i]
	if m.dying > 0 {
		return
	}
	j := 0
	for _, c := range m.camps {
		if c.State == scene.Merged {
			continue
		}
		x, y := p.campAt(m, j, c)
		j++
		if c.State != scene.Failing {
			continue
		}
		for dx := -1; dx <= 1; dx++ {
			for dy := 1; dy <= 2; dy++ {
				lick := .5 + .5*math.Sin(p.t*2.1+float64(dx*3+dy))
				hot := mix(p.pal.flare, p.pal.fire, lick*.3)
				a := (.95 - .3*float64(2-dy)) * (1 - .35*math.Abs(float64(dx)))
				p.color(x+dx, 2*y-3+dy, hot, a)
			}
		}
	}
}

// jets circle each busy defender's fighter round the monster's head, its trail behind it and its name beside.
func (p *painter) jets(i int) {
	m := &p.l.monsters[i]
	if m.dying > 0 {
		return
	}
	busy := 0
	for _, c := range m.team {
		if c.Stance == scene.Climbing {
			busy++
		}
	}
	cy := m.top() + m.h*.3
	ex, ey := m.span()*.75+5, m.h*.32+3
	j := 0
	for _, c := range m.team {
		if c.Stance != scene.Climbing {
			continue
		}
		th := unit(m.seed, 13)*2*math.Pi + 2*math.Pi*float64(j)/float64(busy) + p.t*.09
		j++
		x, y := int(m.x+math.Cos(th)*ex), row(math.Max(p.l.top, cy+math.Sin(th)*ey))
		glyph, dir := "►", 1
		if math.Sin(th) > 0 {
			glyph, dir = "◄", -1
		}
		if !p.mark(x, y, glyph, p.pal.jet) {
			continue
		}
		p.deco(x-dir, y, "·", p.pal.dim)
		side := 1
		if math.Cos(th) < 0 {
			side = -1
		}
		p.name(x, y, side, c.Name, p.pal.jet)
	}
}

// parked lines up the monster's resting crews below it.
func (p *painter) parked(i int) {
	m := &p.l.monsters[i]
	if m.dying > 0 {
		return
	}
	resting, named := 0, ""
	for _, c := range m.team {
		if c.Stance == scene.Resting {
			resting++
			named = c.Name
		}
	}
	if resting == 0 {
		return
	}
	text := "▪ " + strconv.Itoa(resting) + " parked"
	if short, ok := scene.Short(ansi.Strip(named), p.l.agentNames, 16); resting == 1 && ok {
		text = "▪ " + short + " parked"
	}
	n := ansi.StringWidth(text)
	low := row(math.Min(m.water, m.foot)) + 1
	var at [][2]int
	for _, r := range []int{low, low + 1, p.line() + 1, low - 1} {
		at = append(at, [2]int{int(m.x) - n/2, r}, [2]int{int(m.x) - n/2 + n/2, r}, [2]int{int(m.x) - n, r})
	}
	p.label(text, p.pal.dim, at...)
}

// district names a district in the subtitles at the foot of the frame, a chief's nested ones a line above.
func (p *painter) district(g int) {
	d := &p.l.districts[g]
	if d.name == "" {
		return
	}
	p.writer = int32(-g - 1)
	defer func() { p.writer = 0 }()
	y := p.p/2 - 1 - min(d.depth, 1)
	name, ok := scene.Short(d.name, p.l.groupNames, max(scene.ShortMin, int(d.x1-d.x0)-2))
	if !ok {
		return
	}
	color := p.pal.dim
	if d.selected {
		color, name = p.pal.jet, "▾ "+name
	}
	n := ansi.StringWidth(name)
	cx := int((d.x0 + d.x1) / 2)
	var at [][2]int
	for _, dx := range []int{0, -n, n, -2 * n, 2 * n} {
		if x := cx - n/2 + dx; x >= int(d.x0) && x+n <= int(d.x1)+1 {
			at = append(at, [2]int{x, y})
		}
	}
	p.label(name, color, at...)
}

// rain slants down through a storm over the monster.
func (p *painter) rain(m *monster) {
	reach := m.span()*1.1 + 3
	y0, y1 := max(row(p.l.top), row(m.top()-m.h*.1)), row(math.Min(m.water, p.l.bottom-1))
	for y := y0; y <= y1; y++ {
		for x := max(0, int(m.x-reach)); x < min(p.w, int(m.x+reach)+1); x++ {
			if (x+2*y-p.frame*2)%9 == 0 && unit(m.seed+71, x*977+y) < .4 {
				p.deco(x, y, "╱", mix(p.pal.label, p.pal.dim, .5))
			}
		}
	}
}
