package pyramids

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sprites draws the work on the desert: names and alerts, workers, blocks on the ramps, necropolis names and
// the blown sand of storms, the most important first so nothing later writes over it.
func (p *painter) sprites() {
	defer func() { p.writer = 0 }()
	order := reuse(p.bufs.order, len(p.l.ps))
	p.bufs.order = order
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ba, bb := &p.l.ps[a], &p.l.ps[b]
		return cmp.Or(-cmp.Compare(boolf(ba.selected), boolf(bb.selected)), -cmp.Compare(boolf(ba.needsYou), boolf(bb.needsYou)), -cmp.Compare(ba.planned, bb.planned))
	})
	for _, i := range order {
		p.writer = i + 1
		p.crown(i)
	}
	p.writer = 0
	for _, n := range p.l.ns {
		if n.selected {
			p.necropolisName(n)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		b := &p.l.ps[i]
		if b.dying > 0 {
			continue
		}
		if b.weather != scene.Eruption {
			p.crew(i)
		}
		p.ramp(i)
	}
	p.writer = 0
	for _, n := range p.l.ns {
		if !n.selected {
			p.necropolisName(n)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		if b := &p.l.ps[i]; b.weather == scene.Storm && b.dying == 0 {
			p.blown(b)
		}
	}
}

func boolf(b bool) int {
	if b {
		return 1
	}
	return 0
}

// crown is what stands over a pyramid: the gleam of its gold capstone once done, its needs-you beacon, the
// selection marker and its name with how far along it is.
func (p *painter) crown(i int) {
	b := &p.l.ps[i]
	if b.dying > .35 {
		return
	}
	alive := b.dying == 0
	stack := b.crestRow() - 1
	if alive && b.weather == scene.Summit {
		gleam, fg := "✦", p.pal.gold
		if p.frame/4%2 == 1 {
			gleam = "✧"
		}
		p.write(b.mid(), stack, gleam, fg)
		stack--
	}
	color := p.pal.label
	if alive && b.needsYou {
		x, row := p.beaconAt(b)
		fire := "✹"
		if p.frame/4%2 == 1 {
			fire = "·"
		}
		p.mark(x, row, fire, p.pal.red)
		stack = min(stack, row-1)
		color = p.pal.red
	}
	row := stack
	if b.selected && alive {
		p.write(b.mid(), row, "▾", p.pal.rim)
		row--
		color = p.pal.rim
	}
	name := ansi.Strip(b.name)
	if name == "" && b.id != "" && len(b.team)+len(b.camps) > 0 {
		name = strconv.Itoa(b.leaves) + " pyramids"
	}
	if name == "" {
		return
	}
	measured := ""
	if len(b.team)+len(b.camps) > 0 {
		measured = " " + strconv.Itoa(int(math.Round(b.progress*100))) + "%"
	}
	cx := b.mid()
	for _, limit := range []int{min(32, max(10, b.x1-b.x0+4)), 12, 6} {
		text := ansi.Truncate(name, limit, "…") + measured
		n := ansi.StringWidth(text)
		if p.write(min(max(0, cx-n/2), max(0, p.w-n)), row, text, color) {
			return
		}
	}
}

// crew puts the agents on the west ramp, the first at the head of it by the top course and the rest below,
// each named: busy ones haul a sledge of stone up behind them, waiting ones stand red. Resting ones sit on the
// sand at the foot.
func (p *painter) crew(i int) {
	b := &p.l.ps[i]
	top, ground := b.topRow(), b.groundRow()
	var active []scene.Climber
	resting := 0
	restName := ""
	for _, c := range b.team {
		if c.Stance == scene.Resting {
			resting++
			restName = c.Name
		} else {
			active = append(active, c)
		}
	}
	slices.SortStableFunc(active, func(a, c scene.Climber) int { return cmp.Compare(c.Stance, a.Stance) })
	for j, c := range active {
		row := top + j
		x := b.westAt(row)
		if row >= ground {
			p.write(max(0, x-10), ground-1, "+"+strconv.Itoa(len(active)-j)+" hauling", p.pal.worker)
			break
		}
		fg := p.pal.worker
		if c.Stance == scene.Waiting {
			fg = p.pal.red
		}
		if !p.mark(x, row, "♟", fg) {
			continue
		}
		name := x - 1
		if c.Stance == scene.Climbing {
			sledge := "▪"
			if (p.frame/4+j)%2 == 1 {
				sledge = "▫"
			}
			p.put(x-1, row, sledge, p.pal.stone)
			name--
		}
		for _, limit := range []int{18, 8} {
			text := ansi.Truncate(ansi.Strip(c.Name), limit, "…")
			if n := ansi.StringWidth(text); p.write(name-n, row, text, fg) {
				break
			}
		}
	}
	if resting == 0 {
		return
	}
	fg := mix(p.pal.label, p.pal.sand, .25)
	text := strconv.Itoa(resting) + " resting"
	if resting == 1 && restName != "" {
		text = ansi.Truncate(ansi.Strip(restName), 14, "…") + " resting"
	}
	for _, t := range []string{"♙ " + text, "♙ " + strconv.Itoa(resting), "♙"} {
		if p.write(max(0, b.x0-1), ground, t, fg) {
			return
		}
	}
}

// ramp sets each pull request's block on the east ramp at its row: grey stone while open, set green when CI
// passes, cracked red when it fails, and once merged placed in the face, its number beside it.
func (p *painter) ramp(i int) {
	b := &p.l.ps[i]
	for _, k := range b.blocks {
		_, hi := b.edges(k.row)
		x, g, fg := b.eastAt(k.row), "■", p.pal.label
		switch k.state {
		case scene.Green:
			fg = p.pal.green
		case scene.Failing:
			g, fg = "▚", p.pal.red
		case scene.Merged:
			x, g, fg = hi, "▣", p.pal.casing
		}
		ok := false
		if k.state == scene.Green || k.state == scene.Failing {
			ok = p.mark(x, k.row, g, fg)
		} else {
			ok = p.put(x, k.row, g, fg)
		}
		if ok {
			p.write(max(x, hi+1)+1, k.row, ansi.Truncate(ansi.Strip(k.label), 12, "…"), fg)
		}
	}
	if b.more > 0 {
		p.write(b.x1, b.groundRow()-1, "+"+strconv.Itoa(b.more)+" PRs", p.pal.label)
	}
}

// blown is the sand a storm drives sideways across the pyramid, streaming west with each frame.
func (p *painter) blown(b *pyramid) {
	cx, rx := b.cx(), float64(b.x1-b.x0)/2+3
	fg := mix(p.pal.storm, p.pal.haze, .35)
	for y := max(0, (b.base-b.planned-4)/2); y < b.groundRow(); y++ {
		for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)); x++ {
			if (x+5*y+p.frame*2)%13 == 0 && unit(b.seed+71, x*977+y) < .45 {
				p.put(x, y, "~", fg)
			}
		}
	}
}

// necropolisName signs a necropolis on the sand at its foot, or on its plateau when nested. The selected one
// is marked ▸ ◂.
func (p *painter) necropolisName(n necropolis) {
	name := ansi.Strip(n.name)
	if name == "" {
		return
	}
	row := p.hz/2 + 1
	if n.depth > 0 {
		row = p.hz/2 - n.depth
	}
	fg := mix(p.pal.label, p.pal.sand, .2)
	span := max(4, n.x1-n.x0-2)
	if n.selected {
		fg = p.pal.rim
		name = "▸ " + ansi.Truncate(name, max(1, span-4), "…") + " ◂"
	} else {
		name = ansi.Truncate(name, span, "…")
	}
	w := ansi.StringWidth(name)
	mid := (n.x0 + n.x1) / 2
	if !p.write(mid-w/2, row, name, fg) && n.depth == 0 {
		p.write(max(0, n.x0), row, name, fg)
	}
}
