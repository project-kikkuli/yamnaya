package skyline

import (
	"cmp"
	"math"
	"slices"
	"strconv"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// sprites draws the work on the city: names and alerts, workers, pull request floors, district names, cranes
// and rain, the most important first so nothing later writes over it.
func (p *painter) sprites() {
	defer func() { p.writer = 0 }()
	order := make([]int, len(p.l.bs))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		ba, bb := &p.l.bs[a], &p.l.bs[b]
		return cmp.Or(-cmp.Compare(boolf(ba.selected), boolf(bb.selected)), -cmp.Compare(boolf(ba.needsYou), boolf(bb.needsYou)), -cmp.Compare(ba.planned, bb.planned))
	})
	for _, i := range order {
		p.writer = i + 1
		p.crown(i)
	}
	p.writer = 0
	for _, d := range p.l.ds {
		if d.selected {
			p.districtName(d)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		b := &p.l.bs[i]
		if b.dying > 0 {
			continue
		}
		if b.weather != scene.Eruption {
			p.crew(i)
		}
		p.prFloors(i)
	}
	p.writer = 0
	for _, d := range p.l.ds {
		if !d.selected {
			p.districtName(d)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		if b := &p.l.bs[i]; b.crane() {
			p.towerCrane(b)
		}
	}
	for _, i := range order {
		p.writer = i + 1
		if b := &p.l.bs[i]; b.weather == scene.Storm && b.dying == 0 {
			p.rain(b)
		}
	}
}

func boolf(b bool) int {
	if b {
		return 1
	}
	return 0
}

// crown is what stands over a building: its antenna once done, its needs-you beacon, the selection marker and
// its name with how far along it is.
func (p *painter) crown(i int) {
	b := &p.l.bs[i]
	if b.dying > .35 {
		return
	}
	cx := b.cx()
	roof := (b.base - 2*b.built + b.drop()) / 2
	stack := roof - 1
	alive := b.dying == 0
	if alive && b.weather == scene.Summit {
		p.write(cx, roof-1, "│", p.pal.steel)
		light, fg := "✦", p.pal.gold
		if p.frame/4%2 == 1 {
			light, fg = "·", p.pal.steel
		}
		p.write(cx, roof-2, light, fg)
		stack = roof - 3
	}
	if b.crane() {
		stack = min(stack, roof-4)
	}
	color := p.pal.label
	if alive && b.needsYou {
		x, row := p.beaconAt(b)
		light := "◉"
		if p.frame/4%2 == 1 {
			light = "○"
		}
		p.write(x, row+1, "╿", p.pal.red)
		p.write(x, row, light, p.pal.red)
		stack = min(stack, row-1)
		color = p.pal.red
	}
	row := min(stack, (b.base-2*b.planned+b.drop())/2-1)
	if b.selected && alive {
		p.write(cx, row, "▾", p.pal.rim)
		row--
		color = p.pal.rim
	}
	name := ansi.Strip(b.name)
	if name == "" && b.id != "" && len(b.team)+len(b.camps) > 0 {
		name = strconv.Itoa(b.leaves) + " towers"
	}
	if name == "" {
		return
	}
	measured := ""
	if len(b.team)+len(b.camps) > 0 {
		measured = " " + strconv.Itoa(int(math.Round(b.progress*100))) + "%"
	}
	for _, limit := range []int{min(32, max(10, (b.x1-b.x0)*2+4)), 12, 6} {
		text := ansi.Truncate(name, limit, "…") + measured
		n := ansi.StringWidth(text)
		if p.write(min(max(0, cx-n/2), max(0, p.w-n)), row, text, color) {
			return
		}
	}
}

// crew puts the agents on the scaffold up the west face, the first at the top floor being built and the rest
// below, each named: busy ones weld, waiting ones stand red. Resting ones sit on the sidewalk.
func (p *painter) crew(i int) {
	b := &p.l.bs[i]
	roof, ground := (b.base-2*b.built)/2, b.base/2
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
	x := b.x0 - 1
	for j, c := range active {
		row := roof + j
		if row >= ground {
			p.write(max(0, x-9), ground-1, "+"+strconv.Itoa(len(active)-j)+" working", p.pal.worker)
			break
		}
		fg := p.pal.worker
		if c.Stance == scene.Waiting {
			fg = p.pal.red
		}
		if !p.write(x, row, "♟", fg) {
			continue
		}
		if c.Stance == scene.Climbing && (p.frame/4+j)%2 == 0 {
			p.put(b.x0, row, "✶", p.pal.fireHot)
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
	fg := mix(p.pal.label, p.pal.sidewalk, .3)
	text := strconv.Itoa(resting) + " resting"
	if resting == 1 && restName != "" {
		text = ansi.Truncate(ansi.Strip(restName), 14, "…") + " resting"
	}
	for _, t := range []string{"♙ " + text, "♙ " + strconv.Itoa(resting), "♙"} {
		if p.write(max(0, x), ground, t, fg) {
			return
		}
	}
}

// prFloors labels each pull request's floor on the east face: its number in the floor's color.
func (p *painter) prFloors(i int) {
	b := &p.l.bs[i]
	for _, pr := range b.prs {
		row := (b.base - 2*pr.floor - 2) / 2
		g, fg := "▤", p.pal.label
		switch pr.state {
		case scene.Green:
			fg = p.pal.green
		case scene.Failing:
			fg = p.pal.red
		case scene.Merged:
			g, fg = "▣", p.pal.gold
		}
		if p.write(b.x1, row, g, fg) {
			p.write(b.x1+1, row, " "+ansi.Truncate(ansi.Strip(pr.label), 12, "…"), fg)
		}
	}
	if b.more > 0 {
		p.write(b.x1, b.base/2-1, "+"+strconv.Itoa(b.more)+" PRs", p.pal.label)
	}
}

// towerCrane stands on the roof while anyone is busy: a mast, a jib over the east face, and a hook that runs
// along it and lowers a load.
func (p *painter) towerCrane(b *building) {
	roof := (b.base - 2*b.built) / 2
	mx := b.x0 + min(1, b.x1-b.x0-1)
	jib, end := roof-3, b.x1+2
	fg := p.pal.crane
	p.put(mx, roof-1, "╫", fg)
	p.put(mx, roof-2, "╫", fg)
	p.put(mx-2, jib, "▆", fg)
	p.put(mx-1, jib, "═", fg)
	p.put(mx, jib, "╦", fg)
	for x := mx + 1; x <= end; x++ {
		p.put(x, jib, "═", fg)
	}
	run := end - mx - 2
	if run <= 0 {
		return
	}
	k := p.frame / 4 % (2 * run)
	if k > run {
		k = 2*run - k
	}
	tx := mx + 2 + k
	depth := 1
	if tx >= b.x1 {
		depth = 1 + p.frame/4%3
	}
	for y := jib + 1; y < jib+depth+1; y++ {
		p.put(tx, y, "│", p.pal.steel)
	}
	p.put(tx, jib+depth+1, "▣", fg)
}

// rain falls from a storm's cloud past the building to the street.
func (p *painter) rain(b *building) {
	cx, cy, rx, ry := b.stormShape()
	from, to := int(cy+ry)/2, b.base/2
	for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)); x++ {
		for y := from; y <= to; y++ {
			if (x+2*y-p.frame/4*3)%9 == 0 && unit(b.seed+71, x*977+y) < .55 {
				p.put(x, y, "╱", p.pal.rain)
			}
		}
	}
}

// districtName signs a district at its foot: on the road for a repository or chief, on its podium when nested.
// The selected one is marked ▸ ◂.
func (p *painter) districtName(d district) {
	name := ansi.Strip(d.name)
	if name == "" {
		return
	}
	row := p.hz/2 + 1
	if d.depth > 0 {
		row = p.hz/2 - d.depth
	}
	fg := mix(p.pal.label, p.pal.road, .25)
	span := max(4, d.x1-d.x0-2)
	if d.selected {
		fg = p.pal.rim
		name = "▸ " + ansi.Truncate(name, max(1, span-4), "…") + " ◂"
	} else {
		name = ansi.Truncate(name, span, "…")
	}
	n := ansi.StringWidth(name)
	mid := (d.x0 + d.x1) / 2
	if !p.write(mid-n/2, row, name, fg) && d.depth == 0 {
		p.write(max(0, d.x0), row, name, fg)
	}
}
