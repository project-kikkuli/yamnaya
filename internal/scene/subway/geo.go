package subway

import "github.com/project-kikkuli/yamnaya/internal/scene"

// geo is where a line runs: its name tag at the left end of row left, then its track from x0 to x1, exclusive,
// stepping on a 45 degree diagonal at jogX to row right, and ending at its terminus.
type geo struct {
	l           line
	pn          int
	tagX        int
	nameW       int
	left, right int
	x0, x1      int
	jogX, jog   int
	dir         int
	// th is how many pixels thick the band is: a whole cell when lines have room between them, else half.
	th   int
	seed uint64
	hubs []int
	ref  int32
}

func (g *geo) span() int { return g.x1 - g.x0 }

// py is the top pixel of the band at column x.
func (g *geo) py(x int) int {
	d := 0
	if g.jog > 0 {
		d = min(max(x-g.jogX+1, 0), 2*g.jog)
	}
	return 2*g.left + g.dir*d
}

// row is the cell row the band runs through at column x.
func (g *geo) row(x int) int { return (g.py(x) + g.th - 1) / 2 }

// bent reports whether column x is on or next to the diagonal, where no station or sign stands.
func (g *geo) bent(x int) bool { return g.jog > 0 && x >= g.jogX-1 && x <= g.jogX+2*g.jog }

// hubAt reports whether an interchange stands at or next to column x.
func (g *geo) hubAt(x int) bool {
	for _, h := range g.hubs {
		if x >= h-1 && x <= h+1 {
			return true
		}
	}
	return false
}

// built is the column the finished track reaches; past it the line is still being built.
func (g *geo) built() int { return g.x0 + int(g.l.progress*float64(g.span())+.5) }

// shown is the column the track is drawn to: all of it, or less as a kill erases it from the terminus back.
func (g *geo) shown() int {
	if g.l.dying <= 0 {
		return g.x1
	}
	return g.x0 + int((1-g.l.dying)*float64(g.span()))
}

func hubX(x0, x1, depth int) int {
	return min(x1-4, x0+(x1-x0)*(68+12*depth)/100)
}

// geos places every line of every panel.
func geos(ps []panel) []geo {
	var out []geo
	for pi, p := range ps {
		x0, x1 := p.x+p.tagW, p.x+p.w
		for i, l := range p.net.lines {
			t := p.y + 1 + i*p.pitch + (p.pitch-1)/2
			g := geo{l: l, pn: pi, tagX: p.x, nameW: p.nameW, x0: x0, x1: x1, jog: p.jog, dir: p.dir, th: 1, seed: seedOf(l.id)}
			if p.pitch >= 2 {
				g.th = 2
			}
			g.left = t
			if p.dir < 0 {
				g.left = t + p.jog
			}
			g.right = g.left + p.dir*p.jog
			g.jogX = x0 + (x1-x0)*2/5
			for _, h := range p.net.hubs {
				if i >= h.a && i < h.b {
					g.hubs = append(g.hubs, hubX(x0, x1, h.depth))
				}
			}
			out = append(out, g)
		}
	}
	return out
}

// stationX is where a pull request at a stage stands on the track, before it is moved to a free column.
func (g *geo) stationX(stage int) int {
	stage = min(max(stage, 0), 6)
	return g.x0 + 1 + stage*max(1, g.span()-5)/6
}

// slot is the free column nearest x where a thing wide cells across can stand on the track, or -1.
func (g *geo) slot(s *sheet, x, wide int) int {
	for k := 0; k < g.span(); k++ {
		for _, c := range [2]int{x + k, x - k} {
			if g.fits(s, c, wide) {
				return c
			}
			if k == 0 {
				break
			}
		}
	}
	return -1
}

func (g *geo) fits(s *sheet, x, wide int) bool {
	if x < g.x0+1 || x+wide > g.x1-2 {
		return false
	}
	for c := x - 1; c <= x+wide; c++ {
		if g.bent(c) || g.hubAt(c) || !s.free(c, g.row(c)) {
			return false
		}
	}
	return true
}

func stanceCount(team []scene.Climber, st scene.Stance) int {
	n := 0
	for _, c := range team {
		if c.Stance == st {
			n++
		}
	}
	return n
}
