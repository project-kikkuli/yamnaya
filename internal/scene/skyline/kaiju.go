package skyline

import "math"

// kaijuArt is the monster facing west, a pixel per character: # body, e eye, s glowing dorsal spines.
var kaijuArt = [...]string{
	"....#####...........",
	"...##e#####s........",
	"..#########ss.......",
	".##########ss.......",
	".....######sss......",
	"..#####.####ss......",
	"......######sss.....",
	"....#.########s.....",
	"...#..#########s....",
	"......#########s....",
	"......##########s...",
	"......###########...",
	"......###########...",
	".....#############..",
	".....#####..########",
	".....####....###..##",
	"....####.....###....",
	"....###.....####....",
	"...####.....###.....",
	"..#####....#####....",
}

const kaijuW, kaijuH = 20, len(kaijuArt)

// kaiju stomps beside a building whose manager is down: a silhouette taller than the tower, its spines pulsing,
// lifting a foot every half second and now and then breathing a beam into the top floors.
func (p *painter) kaiju(i int) {
	b := &p.l.bs[i]
	floor := float64(2 * b.built)
	h := clamp(1.25*(floor-2), 12, 28)
	k := h / float64(kaijuH)
	left := b.x1 + 3
	raised := p.frame/2%2 == 1
	top := b.base - int(math.Round(h))
	if raised {
		top--
	}
	pulse := .55 + .45*math.Sin(float64(p.frame)*.9)
	rim := mix(p.pal.kaiju, p.pal.fire, .45)
	spine := mix(p.pal.kaiju, p.pal.kaijuGlow, pulse)
	for py := range int(math.Round(h)) {
		sy := min(kaijuH-1, int(float64(py)/k))
		for px := range int(math.Round(kaijuW * k)) {
			sx := min(kaijuW-1, int(float64(px)/k))
			c := kaijuArt[sy][sx]
			if c == '.' {
				continue
			}
			y := top + py
			if raised && sx < 10 && sy >= 15 {
				if sy >= 18 {
					continue
				}
				y -= int(math.Round(2 * k))
			}
			col := p.pal.kaiju
			switch {
			case c == 'e':
				col = p.pal.eye
			case c == 's':
				col = spine
			case kaijuEdge(sx, sy):
				col = rim
			}
			x := left + px
			p.fill(x, y, col)
			if x >= 0 && x < p.w && y >= 0 && y < p.p {
				p.who[y/2*p.w+x] = i + 1
			}
		}
	}
	if !raised {
		for _, foot := range [2]float64{4.5, 13} {
			fx := left + int(foot*k)
			for dx := -3; dx <= 3; dx++ {
				p.blend(fx+dx, b.base-1, p.pal.dust, .5*math.Exp(-float64(dx*dx)/4))
			}
		}
	}
	if f := p.frame % 12; f >= 6 && f < 9 {
		mouth := top + int(4*k)
		hit := b.x1 - 2
		for x := left + int(k); x >= hit; x-- {
			p.set(x, mouth, mix(p.pal.kaijuGlow, white, .5))
			p.blend(x, mouth-1, p.pal.kaijuGlow, .5)
			p.blend(x, mouth+1, p.pal.kaijuGlow, .5)
		}
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				p.blend(hit+dx, mouth+dy, p.pal.fireHot, .8*math.Exp(-float64(dx*dx+dy*dy)/3))
			}
		}
	}
}

// kaijuEdge reports whether a body pixel of the art borders open sky, where the fires light its outline.
func kaijuEdge(x, y int) bool {
	for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		nx, ny := x+d[0], y+d[1]
		if nx < 0 || ny < 0 || nx >= kaijuW || ny >= kaijuH || kaijuArt[ny][nx] == '.' {
			return true
		}
	}
	return false
}
