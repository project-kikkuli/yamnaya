package scene

import (
	"cmp"
	"fmt"
	"math"
	"slices"

	"github.com/charmbracelet/x/ansi"
)

// sprites draws the work on the mountains: names, rope teams, camps and flares, the most important first so
// nothing later writes over it.
func (p *painter) sprites() {
	p.taken = make([]bool, len(p.cells))
	p.who = make([]int, len(p.cells))
	defer func() { p.writer = 0 }()
	order := make([]int, len(p.l.mounts))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ma, mb := p.l.mounts[a], p.l.mounts[b]
		return cmp.Or(-cmp.Compare(boolf(ma.selected), boolf(mb.selected)), -cmp.Compare(boolf(ma.needsYou), boolf(mb.needsYou)), cmp.Compare(p.geo[a].y, p.geo[b].y))
	})
	for _, i := range order {
		p.writer = i + 1
		p.crown(i)
	}
	p.writer = 0
	for _, m := range p.l.massifs {
		if m.selected {
			p.crownRange(m)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		if p.l.mounts[i].weather != Eruption {
			p.team(i)
		}
		p.camps(i)
	}
	p.writer = 0
	for _, m := range p.l.massifs {
		if m.depth == 0 && !m.selected {
			p.rangeName(m)
		}
	}
	for i, m := range p.l.mounts {
		switch m.weather {
		case Storm:
			p.rain(i)
		case Eruption:
			p.embers(i)
		}
	}
}

func cellAbove(y float64) int { return int(math.Ceil(y)-1) / 2 }

// floor is the lowest row of the mountains left in view: the valley, or just above the board.
func (p *painter) floor() int { return p.hz/2 - 1 }

// rowAt is the cell row just above the surface of mount i's layer at column x.
func (p *painter) rowAt(i, x int) int {
	return cellAbove(float64(p.hz) - p.layers[p.l.mounts[i].depth].hm[min(p.w-1, max(0, x))])
}

// flank walks down one side of mount i and gives the column where its surface reaches row.
func (p *painter) flank(i, row, side int) int {
	g := p.geo[i]
	x := int(g.x)
	for step := 0; step < int(g.half); step++ {
		if p.rowAt(i, x+side) > row {
			return x
		}
		x += side
	}
	return x
}

// foot is the lowest row a side of mount i reaches before the ground rises into a neighbor or the board covers it.
func (p *painter) foot(i, side int) int {
	g := p.geo[i]
	x, row := int(g.x), p.rowAt(i, int(g.x))
	for step := 0; step < int(g.half) && x+side >= 0 && x+side < p.w; step++ {
		next := p.rowAt(i, x+side)
		if next < row {
			break
		}
		x, row = x+side, next
	}
	return min(row, p.floor())
}

// crown names a peak above its summit, with a flare when it waits on the owner and a flag when it is done.
func (p *painter) crown(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	x, row := int(g.x), cellAbove(g.y)
	if m.weather == Summit {
		p.write(x, row, "⚑", p.th.flag)
	}
	color := p.th.label
	if m.needsYou {
		lit := p.frame/4%2 == 0
		p.write(x, row-1, "│", p.th.flag)
		star := "✸"
		if !lit {
			star = "✦"
		}
		p.write(x, row-2, star, p.th.flag)
		row -= 2
		color = p.th.flag
	}
	if m.selected {
		p.write(x, row-1, "▾", p.th.rim)
		row--
		color = p.th.rim
	}
	name := ansi.Strip(m.name)
	if len(m.team)+len(m.camps) > 0 && m.id != "" && name == "" {
		name = fmt.Sprintf("%d peaks", m.leaves)
	}
	if name == "" {
		return
	}
	measured := ""
	if len(m.team)+len(m.camps) > 0 {
		measured = fmt.Sprintf(" %d%%", int(math.Round(m.progress*100)))
	}
	for _, limit := range []int{min(32, max(10, int(g.half*2.2))), 12, 6} {
		text := ansi.Truncate(name, limit, "…") + measured
		n := ansi.StringWidth(text)
		if p.write(min(max(0, x-n/2), max(0, p.w-n)), row-1, text, color) {
			return
		}
	}
}

func (p *painter) crownRange(m massif) {
	lay := p.layers[m.depth]
	best, at := 0.0, (m.x0+m.x1)/2
	for x := max(0, int(m.x0)); x < min(p.w, int(m.x1)); x++ {
		if lay.hm[x] > best {
			best, at = lay.hm[x], float64(x)+.5
		}
	}
	row := cellAbove(float64(p.hz) - best)
	p.write(int(at), row, "▾", p.th.rim)
	name := ansi.Truncate(ansi.Strip(m.name), max(4, p.w/3), "…")
	n := ansi.StringWidth(name)
	p.write(min(max(0, int(at)-n/2), max(0, p.w-n)), row-1, name, p.th.rim)
}

func (p *painter) rangeName(m massif) {
	name := ansi.Strip(m.name)
	if name == "" {
		return
	}
	name = ansi.Truncate(name, max(4, int(m.x1-m.x0)-2), "…")
	n := ansi.StringWidth(name)
	p.write(int((m.x0+m.x1)/2)-n/2, p.floor(), name, p.th.cloudShade.scale(1.5))
}

// team ropes the agents up the west flank: the leader as high as the work's progress, the rest one row below
// each other, every one named. Agents stopped on the owner signal in red; resting ones wait at the foot.
func (p *painter) team(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	top, foot := cellAbove(g.y)+1, p.foot(i, -1)
	if foot <= top {
		return
	}
	var active, resting []Climber
	for _, c := range m.team {
		if c.Stance == Resting {
			resting = append(resting, c)
		} else {
			active = append(active, c)
		}
	}
	slices.SortStableFunc(active, func(a, b Climber) int { return cmp.Compare(b.Stance, a.Stance) })
	progress := m.progress
	if m.weather == Summit {
		progress = 1
	}
	row := top + int(math.Round(float64(foot-top)*(1-progress)))
	row = min(row, foot-min(len(active), foot-top)+1)
	for j, c := range active {
		r := row + j
		if r > foot {
			p.tag(i, foot, fmt.Sprintf("+%d climbing", len(active)-j), p.th.climber)
			break
		}
		x := p.flank(i, r, -1)
		glyph, fg := "λ", p.th.climber
		switch {
		case c.Stance == Waiting:
			glyph, fg = "λ", p.th.flag
		case (p.frame/4+j)%2 == 1:
			glyph = "ƛ"
		}
		if !p.write(x, r, glyph, fg) {
			continue
		}
		name := ansi.Truncate(ansi.Strip(c.Name), 18, "…")
		if n := ansi.StringWidth(name); !p.write(x-n-1, r, name, fg) {
			p.write(x+2, r, name, fg)
		}
	}
	if len(resting) > 0 {
		text := fmt.Sprintf("%d resting", len(resting))
		if len(resting) == 1 && resting[0].Name != "" {
			text = ansi.Truncate(ansi.Strip(resting[0].Name), 18, "…") + " resting"
		}
		p.tag(i, foot-1, "▴ "+text, p.th.cloudShade.scale(1.6))
	}
}

// tag writes text at the west foot of mount i, or as close above it as there is room.
func (p *painter) tag(i, row int, text string, fg rgb) {
	n := ansi.StringWidth(text)
	for r := row; r > row-3; r-- {
		x := p.flank(i, r, -1)
		if p.write(x-n, r, text, fg) || p.write(x+1, r, text, fg) {
			return
		}
	}
}

// camps pitches each pull request on the east flank at the height of the gates it passed; merged ones fly at the top.
func (p *painter) camps(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	top, foot := cellAbove(g.y)+1, p.foot(i, 1)
	if foot <= top || len(m.camps) == 0 {
		return
	}
	var camps []Camp
	merged := 0
	for _, c := range m.camps {
		if c.State == Merged {
			merged++
			if merged == 1 {
				camps = append(camps, c)
			}
		} else {
			camps = append(camps, c)
		}
	}
	slices.SortStableFunc(camps, func(a, b Camp) int { return cmp.Compare(b.Stage, a.Stage) })
	used := map[int]bool{}
	for k, c := range camps {
		if c.State == Merged && merged > 1 {
			c.Label = fmt.Sprintf("%d merged", merged)
		}
		row := top + int(math.Round(float64(foot-top)*(1-float64(min(c.Stage, 6))/6)))
		for used[row] && row <= foot {
			row++
		}
		if row > foot {
			n := len(camps) - k
			p.write(p.flank(i, foot, 1)+1, foot, fmt.Sprintf("+%d PRs", n), p.th.label)
			return
		}
		used[row] = true
		x := p.flank(i, row, 1)
		glyph, fg := "▲", p.th.label
		switch c.State {
		case Green:
			fg = p.th.green
		case Failing:
			fg = p.th.flag
		case Merged:
			glyph, fg = "⚑", p.th.gold
		}
		if p.write(x, row, glyph, fg) {
			p.write(x+2, row, ansi.Truncate(ansi.Strip(c.Label), 14, "…"), fg)
		}
	}
}

func (p *painter) rain(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	hm := p.layers[m.depth].hm
	rx := g.half * .7
	cloud := int(g.y+math.Max(4, g.height*.38)*.6) / 2
	for x := max(0, int(g.x-rx)); x < min(p.w, int(g.x+rx)); x++ {
		ground := cellAbove(float64(p.hz) - hm[x])
		for y := cloud; y <= ground+1; y++ {
			if (x+2*y-p.frame/4*3)%11 == 0 && unit(m.seed+71, x*977+y) < .5 {
				p.write(x, y, "╱", p.th.stormTop)
			}
		}
	}
}

func (p *painter) embers(i int) {
	g := p.geo[i]
	for k := range 7 {
		age := math.Mod(p.t*.8+float64(k)*3.7, 26)
		x := g.x + age*.25 + math.Sin(float64(k)*3+age*.3)*age*.3
		y := g.y - 2 - age*.9
		if y < 0 {
			continue
		}
		p.write(int(x), int(y)/2, "·", mix(p.th.lavaHot, p.th.lava, age/26))
	}
}
