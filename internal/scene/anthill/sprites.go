package anthill

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sprites draws the life of the colony: names and alarms, ants, chambers, colony names, rain and alates, the
// most important first so nothing later writes over it.
func (p *painter) sprites() {
	defer func() { p.writer = 0 }()
	order := make([]int, len(p.l.ms))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ma, mb := &p.l.ms[a], &p.l.ms[b]
		return cmp.Or(-cmp.Compare(boolf(ma.selected), boolf(mb.selected)), -cmp.Compare(boolf(ma.needsYou), boolf(mb.needsYou)), -cmp.Compare(ma.height, mb.height))
	})
	for _, i := range order {
		p.writer = i + 1
		p.crown(i)
	}
	p.writer = 0
	for _, c := range p.l.cs {
		if c.selected {
			p.colonyName(c)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		if m := &p.l.ms[i]; m.dying == 0 {
			p.crew(m)
			p.stores(m)
		}
	}
	p.writer = 0
	for _, c := range p.l.cs {
		if !c.selected {
			p.colonyName(c)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		m := &p.l.ms[i]
		switch {
		case m.dying > 0:
		case m.weather == scene.Storm:
			p.rain(m)
		case m.weather == scene.Summit:
			p.alates(m)
		}
	}
}

func boolf(b bool) int {
	if b {
		return 1
	}
	return 0
}

// crown is what stands over a mound's entrance: the blinking alarm ant, the selection marker, and its name with
// how far along it is.
func (p *painter) crown(i int) {
	m := &p.l.ms[i]
	if m.dying > .35 {
		return
	}
	alive := m.dying == 0
	row := m.entry(p.g)/2 - 1
	color := p.pal.label
	if alive && m.needsYou {
		ant := "Ж"
		if p.frame/4%2 == 1 {
			ant = "ж"
		}
		p.signal(m.cx, row, ant, p.pal.red)
		row--
		color = p.pal.red
	}
	if alive && m.selected {
		p.write(m.cx, row, "▾", p.pal.rim)
		row--
		color = p.pal.rim
	}
	name := ansi.Strip(m.name)
	if name == "" && m.id != "" && len(m.team)+len(m.camps) > 0 {
		name = strconv.Itoa(m.leaves) + " mounds"
	}
	if name == "" {
		return
	}
	measured := ""
	if len(m.team)+len(m.camps) > 0 {
		measured = " " + strconv.Itoa(int(math.Round(m.progress*100))) + "%"
	}
	for _, limit := range []int{min(32, max(10, (m.x1-m.x0)*2+4)), 12, 6} {
		text := ansi.Truncate(name, limit, "…") + measured
		n := ansi.StringWidth(text)
		if p.write(min(max(0, m.cx-n/2), max(0, p.w-n)), row, text, color) {
			return
		}
	}
}

// crew puts the ants in the nest: waiting ones stand red and still beside the top of the shaft with their names,
// busy ones march up and down the shaft and along the gallery, resting ones curl up in the brood comb.
func (p *painter) crew(m *mound) {
	surface := p.g.sy / 2
	j := 0
	for _, c := range m.team {
		if c.Stance != scene.Waiting {
			continue
		}
		row := surface + j
		j++
		if row >= m.bottom/2 || !p.signal(m.cx-1, row, "ж", p.pal.red) {
			continue
		}
		for _, limit := range []int{14, 6} {
			name := ansi.Truncate(ansi.Strip(c.Name), limit, "…")
			if n := ansi.StringWidth(name); p.write(m.cx-2-n, row, name, p.pal.red) {
				break
			}
		}
	}
	busy := m.count(scene.Climbing)
	top, floor := m.entry(p.g)/2, m.bottom/2
	run, dir := 0, 1
	if m.parent >= 0 {
		c := p.l.cs[m.parent]
		if run = min(6, c.gx1-m.cx); run < 2 {
			run, dir = min(6, m.cx-c.gx0), -1
		}
	}
	shaft := floor - top
	length := shaft + max(0, run)
	for k := range busy {
		at := (p.frame + k*2*length/max(1, busy)) % max(1, 2*length)
		if at >= length {
			at = 2*length - 1 - at
		}
		x, y := m.cx, top+at
		if at >= shaft {
			x, y = m.cx+dir*(at-shaft+1), floor
		}
		p.signal(x, y, "ж", p.pal.ant)
	}
	if resting := m.count(scene.Resting); resting > 0 {
		_, cy, _, _ := m.nest(p.g)
		row := max(surface, int(cy)/2)
		if p.signal(m.cx+1, row, "ж", p.pal.rest) {
			p.write(m.cx+2, row, " "+strconv.Itoa(resting)+" resting", p.pal.rest)
		}
	}
}

// stores marks each chamber with its pull request's state and labels it on the far side.
func (p *painter) stores(m *mound) {
	for _, c := range m.chambers {
		g, fg := "○", p.pal.open
		switch c.state {
		case scene.Green:
			g, fg = "●", p.pal.green
		case scene.Failing:
			g, fg = "▒", p.pal.red
		case scene.Merged:
			g, fg = "◆", p.pal.gold
		}
		x, row := m.cx+3*c.side, c.y/2
		if !p.write(x, row, g, fg) {
			continue
		}
		label := ansi.Truncate(ansi.Strip(c.label), 10, "…")
		if c.side > 0 {
			p.write(x+2, row, label, fg)
		} else {
			p.write(x-1-ansi.StringWidth(label), row, label, fg)
		}
	}
	if m.more > 0 {
		p.write(m.cx+1, (p.g.sy+p.g.nest)/2, "+"+strconv.Itoa(m.more)+" PRs", p.pal.ink)
	}
}

// rain falls from a flooded mound's cloud to the ground.
func (p *painter) rain(m *mound) {
	cx, cy, rx, ry := p.stormCloud(m)
	for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)); x++ {
		for y := int(cy+ry) / 2; y < p.g.sy/2; y++ {
			if (x+2*y-p.frame*3)%7 == 0 && unit(m.seed+71, x*977+y) < .6 {
				p.put(x, y, "╱", p.pal.rain)
			}
		}
	}
}

// alates are the winged ants of a full mound, flying off over it.
func (p *painter) alates(m *mound) {
	top := m.entry(p.g)/2 - 3
	span := float64(m.x1-m.x0) * .9
	for k := range 5 {
		x := float64(m.cx) + (unit(m.seed, 40+k)-.5)*span*2 + math.Sin(float64(p.frame)*.5+float64(k))*1.5
		y := top - k%3 - (p.frame/3+k)%3
		p.put(int(math.Round(x)), max(0, y), "ψ", p.pal.alate)
	}
}

// colonyName signs a colony under its gallery; the selected one reads ▸ name ◂.
func (p *painter) colonyName(c colony) {
	name := ansi.Strip(c.name)
	if name == "" {
		return
	}
	row := p.g.gallery(c.depth)/2 + 1
	fg := p.pal.ink
	span := max(4, c.x1-c.x0-2)
	if c.selected {
		fg = p.pal.inkRim
		name = "▸ " + ansi.Truncate(name, max(1, span-4), "…") + " ◂"
	} else {
		name = ansi.Truncate(name, span, "…")
	}
	n := ansi.StringWidth(name)
	mid := (c.gx0 + c.gx1) / 2
	if !p.write(mid-n/2, row, name, fg) {
		p.write(max(0, c.x0), row, name, fg)
	}
}
