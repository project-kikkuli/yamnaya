package forest

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sprites draws the work on the forest: names and alerts, agents, nests, grove names and rain, the most
// important first so nothing later writes over it.
func (p *painter) sprites() {
	defer func() { p.writer = 0 }()
	order := make([]int, len(p.l.ts))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ta, tb := &p.l.ts[a], &p.l.ts[b]
		return cmp.Or(-cmp.Compare(boolf(ta.selected), boolf(tb.selected)), -cmp.Compare(boolf(ta.needsYou), boolf(tb.needsYou)), -cmp.Compare(ta.h, tb.h))
	})
	for _, i := range order {
		p.writer = i + 1
		p.crest(i)
	}
	p.writer = 0
	for _, g := range p.l.gs {
		if g.selected {
			p.groveName(g)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		t := &p.l.ts[i]
		if t.dying > 0 {
			continue
		}
		if t.weather != scene.Eruption {
			p.climbers(i)
		}
		p.nests(i)
	}
	p.writer = 0
	for _, g := range p.l.gs {
		if !g.selected {
			p.groveName(g)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		if t := &p.l.ts[i]; t.weather == scene.Storm && t.dying == 0 {
			p.rain(t)
		}
	}
}

// crest is what hangs on and over a tree: the blossom on top once it is done, its needs-you lantern, the
// selection marker and its name with how far along it is.
func (p *painter) crest(i int) {
	t := &p.l.ts[i]
	if t.dying > .3 {
		return
	}
	alive := t.dying == 0
	stack := t.top() - 1
	if alive && t.weather == scene.Summit {
		p.write(t.cx, stack, "✿", p.pal.blossom)
		stack--
	}
	color := p.pal.label
	if alive && t.needsYou {
		x, row := t.lantern()
		light := "◆"
		if p.frame/4%2 == 1 {
			light = "◇"
		}
		p.write(x, row-1, "╷", p.pal.nest)
		p.signal(x, row, light, p.pal.red)
		stack = min(stack, row-2)
		color = p.pal.red
	}
	row := stack
	if t.selected && alive {
		p.write(t.cx, row, "▾", p.pal.rim)
		row--
		color = p.pal.rim
	}
	name := ansi.Strip(t.name)
	if name == "" && t.id != "" && len(t.team)+len(t.camps) > 0 {
		name = strconv.Itoa(t.leaves) + " trees"
	}
	if name == "" {
		return
	}
	measured := ""
	if len(t.team)+len(t.camps) > 0 {
		measured = " " + strconv.Itoa(int(math.Round(t.progress*100))) + "%"
	}
	for _, limit := range []int{min(32, max(10, (t.x1-t.x0)*2)), 12, 6} {
		text := ansi.Truncate(name, limit, "…") + measured
		n := ansi.StringWidth(text)
		if p.write(min(max(0, t.cx-n/2), max(0, p.w-n)), row, text, color) {
			return
		}
	}
}

// climbers puts the agents on the west of the trunk, the first high in the crown and the rest below, each
// named: busy birds flap and busy squirrels scurry, a waiting bird sits still and red. Resting ones curl up
// in the roots.
func (p *painter) climbers(i int) {
	t := &p.l.ts[i]
	top, ground := t.top()+1, t.ground()
	var active []scene.Climber
	resting := 0
	restName := ""
	for _, c := range t.team {
		if c.Stance == scene.Resting {
			resting++
			restName = c.Name
		} else {
			active = append(active, c)
		}
	}
	slices.SortStableFunc(active, func(a, c scene.Climber) int { return cmp.Compare(c.Stance, a.Stance) })
	gap := 1
	if ground-top+1 >= 2*len(active) {
		gap = 2
	}
	x := t.cx - t.tw - 1
	beat := p.frame / 4
	for j, c := range active {
		row := top + j*gap
		if row > ground {
			p.write(max(0, x-8), ground, "+"+strconv.Itoa(len(active)-j)+" busy", p.pal.agent)
			break
		}
		fg, g := p.pal.agent, "v"
		switch {
		case c.Stance == scene.Waiting:
			fg = p.pal.red
		case j%2 == 1:
			g = "∂"
			if gap == 2 {
				row += (beat + j) % 2
			}
		case (beat+j)%2 == 1:
			g = "ʌ"
		}
		if !p.signal(x, row, g, fg) {
			continue
		}
		for _, limit := range []int{18, 8} {
			name := ansi.Truncate(ansi.Strip(c.Name), limit, "…")
			if n := ansi.StringWidth(name); p.write(x-n-1, row, name, fg) {
				break
			}
		}
	}
	if resting == 0 {
		return
	}
	fg := mix(p.pal.label, p.pal.grass, .3)
	text := strconv.Itoa(resting) + " resting"
	if resting == 1 && restName != "" {
		text = ansi.Truncate(ansi.Strip(restName), 14, "…") + " resting"
	}
	row := t.base / 2
	for _, s := range []string{"zz " + text, "zz " + strconv.Itoa(resting), "zz"} {
		if n := ansi.StringWidth(s); p.write(max(0, x+1-n), row, s, fg) {
			return
		}
	}
}

// nests hangs each pull request's nest on the east of the trunk with its number: a twig nest while open,
// glowing green when CI passes, red and wilted when it fails, a blossom once merged.
func (p *painter) nests(i int) {
	t := &p.l.ts[i]
	x := t.cx + t.tw + 1
	for _, n := range t.nests {
		g, fg := "◡", p.pal.nest
		ok := false
		switch n.state {
		case scene.Green:
			fg = p.pal.green
		case scene.Failing:
			g, fg = "◠", p.pal.red
			ok = p.signal(x, n.row, g, fg)
		case scene.Merged:
			g, fg = "✿", p.pal.blossom
		}
		if n.state != scene.Failing {
			ok = p.write(x, n.row, g, fg)
		}
		if ok {
			label := " " + ansi.Truncate(ansi.Strip(n.label), 12, "…")
			if !p.write(x+1, n.row, label, fg) {
				p.write(x+1, n.row, " "+ansi.Truncate(ansi.Strip(n.label), 5, "…"), fg)
			}
		}
	}
	if t.more > 0 {
		p.write(x, t.ground(), "+"+strconv.Itoa(t.more)+" PRs", p.pal.label)
	}
}

// rain blows slantwise from a storm's cloud past the tree to the ground.
func (p *painter) rain(t *tree) {
	cx, cy, rx, ry := t.stormShape()
	from, to := int(cy+ry)/2, t.base/2
	for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)); x++ {
		for y := from; y <= to; y++ {
			if (x+2*y-p.frame/4*3)%9 == 0 && unit(t.seed+71, x*977+y) < .55 {
				p.put(x, y, "╱", p.pal.rain)
			}
		}
	}
}

// groveName signs a grove or clearing on the ground at its foot, a nested one a row lower per level. The
// selected one is marked ▸ ◂.
func (p *painter) groveName(g grove) {
	name := ansi.Strip(g.name)
	if name == "" {
		return
	}
	row := p.hz/2 + 1 + g.depth
	fg := mix(p.pal.label, p.pal.soil, .2)
	span := max(4, g.x1-g.x0-2)
	if g.selected {
		fg = p.pal.rim
		name = "▸ " + ansi.Truncate(name, max(1, span-4), "…") + " ◂"
	} else {
		name = ansi.Truncate(name, span, "…")
	}
	n := ansi.StringWidth(name)
	mid := (g.x0 + g.x1) / 2
	if !p.write(mid-n/2, row, name, fg) && g.depth == 0 {
		p.write(max(0, g.x0), row, name, fg)
	}
}
