package scene

import (
	"math"

	"github.com/charmbracelet/x/ansi"
)

func (p *painter) sprites() {
	for i, m := range p.l.mounts {
		switch m.weather {
		case Storm:
			p.rain(i)
		case Eruption:
			p.embers(i)
		}
		if m.weather == Summit {
			p.put(int(p.geo[i].x), cellAbove(p.geo[i].y), '⚑', p.th.flag)
		}
		if m.weather != Eruption && m.climbers > 0 {
			p.team(i)
		}
	}
	for i, m := range p.l.mounts {
		if m.selected {
			row := cellAbove(p.geo[i].y)
			if m.weather == Summit {
				row--
			}
			p.mark(p.geo[i].x, row, m.name)
		}
	}
	for _, m := range p.l.massifs {
		if !m.selected {
			continue
		}
		lay := p.layers[m.depth]
		best, at := 0.0, (m.x0+m.x1)/2
		for x := max(0, int(m.x0)); x < min(p.w, int(m.x1)); x++ {
			if lay.hm[x] > best {
				best, at = lay.hm[x], float64(x)+.5
			}
		}
		p.mark(at, cellAbove(float64(p.hz)-best), m.name)
	}
}

func cellAbove(y float64) int { return int(math.Ceil(y)-1) / 2 }

// mark points at the selection and names it.
func (p *painter) mark(x float64, row int, name string) {
	p.put(int(x), row, '▾', p.th.rim)
	if name == "" || row < 1 {
		return
	}
	name = ansi.Truncate(ansi.Strip(name), max(4, p.w/3), "…")
	n := ansi.StringWidth(name)
	start := min(max(0, int(x)-n/2), max(0, p.w-n))
	p.label(start, row-1, name, p.th.label)
}

// team is the rope team on a peak: the leader as high as the goal's progress, the rest roped below.
func (p *painter) team(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	hm := p.layers[m.depth].hm
	shown := min(m.climbers, max(1, int(g.half*.3)), 7)
	progress := m.progress
	if m.weather == Summit {
		progress = 1
	}
	foot := g.x - g.half*.8
	lead := foot + (g.x-1-foot)*progress
	prev := -1
	for j := range shown {
		x := int(lead - float64(j)*2.4)
		if x < 0 || x >= p.w {
			continue
		}
		row := cellAbove(float64(p.hz) - hm[x])
		fg := p.th.climber
		if p.th.night {
			fg = mix(p.th.rope, p.th.climber, .65+.35*math.Sin(p.t*.4-float64(j)))
		}
		p.put(x, row, 'λ', fg)
		if prev >= 0 {
			for rx := x + 1; rx < prev; rx++ {
				p.put(rx, cellAbove(float64(p.hz)-hm[rx]), '·', p.th.rope)
			}
		}
		prev = x
	}
}

func (p *painter) rain(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	hm := p.layers[m.depth].hm
	rx := g.half * 1.1
	cloud := int(g.y+math.Max(4, g.height*.38)*.6) / 2
	for x := max(0, int(g.x-rx)); x < min(p.w, int(g.x+rx)); x++ {
		ground := cellAbove(float64(p.hz) - hm[x])
		for y := cloud; y <= ground+1; y++ {
			if (x+2*y-p.frame)%7 == 0 && unit(m.seed+71, x*977+y) < .75 {
				p.put(x, y, '╱', p.th.stormTop)
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
		p.put(int(x), int(y)/2, '·', mix(p.th.lavaHot, p.th.lava, age/26))
	}
}
