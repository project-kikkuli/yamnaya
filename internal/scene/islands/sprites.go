package islands

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sprites draws the work over the sea, the most important first so nothing later writes over it: names and
// alerts, boats waiting on the owner, buildings, boats at work, moorings, then weather and the swell's crests.
func (p *painter) sprites() {
	defer func() { p.writer = 0 }()
	order := make([]int, len(p.l.isles))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ia, ib := &p.l.isles[a], &p.l.isles[b]
		return cmp.Or(-cmp.Compare(boolf(ia.selected), boolf(ib.selected)), -cmp.Compare(boolf(ia.needsYou), boolf(ib.needsYou)), cmp.Compare(ia.cy, ib.cy))
	})
	for _, i := range order {
		p.writer = int32(i + 1)
		p.crown(i)
	}
	for g := range p.l.groups {
		if p.l.groups[g].selected {
			p.groupName(g)
		}
	}
	for _, i := range order {
		p.writer = int32(i + 1)
		p.waiting(i)
	}
	for _, i := range order {
		p.writer = int32(i + 1)
		p.buildings(i)
	}
	for _, i := range order {
		p.writer = int32(i + 1)
		p.sailing(i)
		p.moored(i)
	}
	for g := range p.l.groups {
		if !p.l.groups[g].selected {
			p.groupName(g)
		}
	}
	p.writer = 0
	for i := range p.l.isles {
		switch is := &p.l.isles[i]; {
		case is.dying > 0:
		case is.weather == scene.Storm:
			p.rain(i)
		case is.weather == scene.Eruption:
			p.embers(i)
		}
	}
	p.crests()
	p.glint()
}

func boolf(b bool) int {
	if b {
		return 1
	}
	return 0
}

// top is the cell row of island is's northern shore.
func (is *isle) top() int { return int(is.cy-is.r*edge(is.seed, 0, -1)) / 2 }

// bottom is the cell row of island is's southern shore.
func (is *isle) bottom() int { return int(is.cy+is.r*edge(is.seed, 0, 1)) / 2 }

// crown names an island above its north shore, with its done percentage, an SOS flare when it waits on the
// owner, the selection's marker, and on a summit a flag on its peak.
func (p *painter) crown(i int) {
	is := &p.l.isles[i]
	if is.dying > .6 {
		return
	}
	x, row := int(is.cx), is.top()-1
	color := p.pal.label
	if is.dying == 0 {
		if is.weather == scene.Summit {
			p.write(x, int(is.cy)/2, "⚑", p.pal.rim)
		}
		if is.needsYou {
			star := "✸ SOS"
			if p.frame/4%2 == 1 {
				star = "✦ SOS"
			}
			p.mark(x-2, row-1, star, p.pal.flare)
			color = p.pal.flare
		}
		if is.selected {
			up := row - 1
			if is.needsYou {
				up--
			}
			p.write(x, up, "▾", p.pal.rim)
			if !is.needsYou {
				color = p.pal.rim
			}
		}
	}
	name := is.name
	if name == "" && len(is.team)+len(is.camps) > 0 {
		name = strconv.Itoa(is.leaves) + " islands"
	}
	if name == "" {
		return
	}
	measured := ""
	if len(is.team)+len(is.camps) > 0 {
		measured = " " + strconv.Itoa(int(math.Round(is.progress*100))) + "%"
	}
	for _, limit := range []int{min(32, max(10, int(is.r*2.6))), 12, 6} {
		text := ansi.Truncate(name, limit, "…") + measured
		n := ansi.StringWidth(text)
		p.keep = color == p.pal.flare
		ok := p.write(min(max(0, x-n/2), max(0, p.w-n)), row, text, color)
		p.keep = false
		if ok {
			return
		}
	}
}

// groupName writes an archipelago's name under it, or a chief's in its lagoon; the selected one lit and marked.
func (p *painter) groupName(gi int) {
	g := &p.l.groups[gi]
	if g.name == "" {
		return
	}
	p.writer = int32(-gi - 1)
	defer func() { p.writer = 0 }()
	x, row := (g.area.x0+g.area.x1)/2, int(g.area.y1)/2-1
	if g.la > 0 {
		x, row = g.lx, int(g.ly)/2
	}
	color := p.pal.dim
	name := ansi.Truncate(g.name, max(4, int(g.area.w())-4), "…")
	if g.selected {
		color, name = p.pal.rim, "▾ "+name
	}
	n := ansi.StringWidth(name)
	for _, r := range []int{row, row - 1, row + 1} {
		if p.write(min(max(0, int(x)-n/2), max(0, p.w-n)), r, name, color) {
			return
		}
	}
}

// orbit is the cell at bearing th on island is's ring of water, out by the extra pixels.
func (is *isle) orbit(th, out float64) (int, int) {
	ro := is.r*1.2 + out
	return int(is.cx + ro*math.Cos(th)), int(is.cy+ro*math.Sin(th)*.9) / 2
}

// waiting moors each boat stopped on the owner off island i's south shore, red under a red flare.
func (p *painter) waiting(i int) {
	is := &p.l.isles[i]
	if is.dying > 0 {
		return
	}
	j := 0
	for _, c := range is.team {
		if c.Stance != scene.Waiting {
			continue
		}
		for try := range 8 {
			th := math.Pi/2 + float64(j+try)*.42*float64(1-2*(try%2))
			x, y := is.orbit(th, 2)
			if !p.free(x, y-1) || !p.mark(x, y, "▸", p.pal.flare) {
				continue
			}
			p.mark(x, y-1, "✶", p.pal.flare)
			p.keep = true
			p.label(x, y, math.Cos(th), c.Name, p.pal.flare)
			p.keep = false
			break
		}
		j++
	}
}

// sailing sends island i's busy boats round it, each with its wake and its agent's name.
func (p *painter) sailing(i int) {
	is := &p.l.isles[i]
	if is.dying > 0 {
		return
	}
	busy := 0
	for _, c := range is.team {
		if c.Stance == scene.Climbing {
			busy++
		}
	}
	j := 0
	for _, c := range is.team {
		if c.Stance != scene.Climbing {
			continue
		}
		th := unit(is.seed, 13)*6.28 + 2*math.Pi*float64(j)/float64(busy) + p.t*.045
		j++
		x, y := is.orbit(th, 3)
		glyph, dir := "▸", 1
		if math.Sin(th) > 0 {
			glyph, dir = "◂", -1
		}
		if !p.write(x, y, glyph, p.pal.boat) {
			continue
		}
		p.write(x-dir, y, "·", mix(p.pal.foam, p.pal.boat, .3))
		p.label(x, y, math.Cos(th), c.Name, p.pal.boat)
	}
}

// label writes an agent's name beside its boat, on the side away from the island when there is room.
func (p *painter) label(x, y int, out float64, name string, fg rgb) {
	name = ansi.Truncate(ansi.Strip(name), 16, "…")
	n := ansi.StringWidth(name)
	if out >= 0 {
		_ = p.write(x+2, y, name, fg) || p.write(x-n-1, y, name, fg)
		return
	}
	_ = p.write(x-n-1, y, name, fg) || p.write(x+2, y, name, fg)
}

// moored writes island i's resting agents under its south shore.
func (p *painter) moored(i int) {
	is := &p.l.isles[i]
	if is.dying > 0 {
		return
	}
	resting, named := 0, ""
	for _, c := range is.team {
		if c.Stance == scene.Resting {
			resting++
			named = c.Name
		}
	}
	if resting == 0 {
		return
	}
	text := "▪ " + strconv.Itoa(resting) + " resting"
	if resting == 1 && named != "" {
		text = "▪ " + ansi.Truncate(ansi.Strip(named), 16, "…") + " resting"
	}
	n := ansi.StringWidth(text)
	for _, row := range []int{is.bottom() + 1, is.bottom() + 2, is.bottom()} {
		if p.write(int(is.cx)-n/2, row, text, p.pal.dim) {
			return
		}
	}
}

// buildings raises island i's pull requests: a dock at the shore for few gates passed, further inland the more,
// lit when CI is green and damaged when it fails; merged ones share a lighthouse on the shore. A summit fills
// the harbor with piers all round.
func (p *painter) buildings(i int) {
	is := &p.l.isles[i]
	if is.dying > 0 {
		return
	}
	base := unit(is.seed, 14) * 6.28
	merged, label := 0, ""
	for k, c := range is.camps {
		if c.State == scene.Merged {
			merged++
			label = c.Label
			continue
		}
		th := base + float64(k)*2.39996
		cs, sn := math.Cos(th), math.Sin(th)
		f := 1.08 - float64(min(max(c.Stage, 0), 6))/6*.94
		re := is.r * edge(is.seed, cs, sn) * f
		x, y := int(is.cx+cs*re), int(is.cy+sn*re)/2
		glyph, fg := "□", p.pal.open
		switch c.State {
		case scene.Green:
			glyph, fg = "■", p.pal.green
		case scene.Failing:
			glyph, fg = "⌧", p.pal.flare
		}
		if p.mark(x, y, glyph, fg) {
			p.mark(x+1, y, ansi.Truncate(ansi.Strip(c.Label), 7, "…"), fg)
		}
	}
	if merged > 0 {
		th := base + math.Pi
		cs, sn := math.Cos(th), math.Sin(th)
		re := is.r * edge(is.seed, cs, sn)
		x, y := int(is.cx+cs*re), int(is.cy+sn*re)/2
		if merged > 1 {
			label = strconv.Itoa(merged) + " merged"
		}
		if p.mark(x, y, "♜", p.pal.gold) {
			beam := [...]string{"─", "╲", "│", "╱"}[p.frame/2%4]
			p.mark(x+1, y, beam, mix(p.pal.gold, scene.White, .5))
			p.mark(x+2, y, ansi.Truncate(ansi.Strip(label), 10, "…"), p.pal.gold)
		}
	}
	if is.weather == scene.Summit {
		for k := range 8 {
			th := base + .4 + float64(k)*math.Pi/4
			cs, sn := math.Cos(th), math.Sin(th)
			re := is.r*edge(is.seed, cs, sn) + 1.5
			p.write(int(is.cx+cs*re), int(is.cy+sn*re)/2, "╤", p.pal.gold)
		}
	}
}

// rain falls slantwise under island i's storm cell, with a bolt when the lightning flashes.
func (p *painter) rain(i int) {
	is := &p.l.isles[i]
	reach := is.r*1.3 + 2
	x0, x1 := max(0, int(is.cx-reach)), min(p.w, int(is.cx+reach)+1)
	y0, y1 := max(0, int(is.cy-reach)/2), min(p.p/2, int(is.cy+reach)/2+1)
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			if (x+2*y-p.frame*2)%7 == 0 && unit(is.seed+71, x*977+y) < .5 &&
				math.Hypot(float64(x)+.5-is.cx, float64(2*y+1)-is.cy) < reach {
				p.write(x, y, "╱", mix(p.pal.rim, p.pal.storm, .35))
			}
		}
	}
	if p.flash(is) {
		p.write(int(is.cx+(unit(is.seed, p.frame/14)-.5)*is.r), int(is.cy)/2-1, "ϟ", p.pal.bolt)
	}
}

func (p *painter) embers(i int) {
	is := &p.l.isles[i]
	for k := range 6 {
		age := math.Mod(p.t*.8+float64(k)*3.7, 20)
		x := is.cx + age*.5 + math.Sin(float64(k)*3+age*.3)*age*.25
		y := is.cy - 1 - age*.8
		if y < 0 {
			continue
		}
		p.write(int(x), int(y)/2, "·", mix(p.pal.lavaHot, p.pal.lava, age/20))
	}
}

// crests roll wave tops across the open sea, each keeping its row as it travels west.
func (p *painter) crests() {
	shift := p.frame / 2
	for _, c := range p.waves {
		x, y := int(c)%p.w, int(c)/p.w
		v := x + 3*y + shift
		if v%29 != 0 || unit(0xa1, v/29*131+y) > .3 {
			continue
		}
		p.write(x, y, "~", mix(p.px[2*y*p.w+x], p.pal.foam, .45))
	}
}

// glint sparkles on the water where the sun or moon catches it.
func (p *painter) glint() {
	cx, cy := int(float64(p.w)*.8), int(float64(p.p/2)*.16)
	for y := max(0, cy-2); y <= cy+2; y++ {
		for x := max(0, cx-7); x < min(p.w, cx+8); x++ {
			k := y*p.w + x
			if p.field[2*y*p.w+x] > -.7 || unit(0xb2, k+p.frame/3*7919) > .12 {
				continue
			}
			p.write(x, y, "·", p.pal.glint)
		}
	}
}
