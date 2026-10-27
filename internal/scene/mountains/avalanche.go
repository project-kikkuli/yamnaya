package mountains

import "math"

// avalanche buries a killed peak: snow pours from the summit down its faces as the kill plays, a powder cloud
// rolling ahead of it, and at the end the buried peak fades into the sky behind it.
func (p *painter) avalanche(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	d := m.dying
	lay := p.layers[m.depth]
	front := g.y + d*1.2*g.height
	snow, powder := mix(p.th.snowLit, white, .5), mix(p.th.snowShade, white, .7)
	for x := max(0, int(g.x-g.half*1.3)); x < min(p.w, int(g.x+g.half*1.3)+1); x++ {
		if lay.own[x] != i {
			continue
		}
		surface := float64(p.hz) - lay.hm[x]
		spread := math.Abs(float64(x)+.5-g.x) / g.half
		reach := front - spread*g.height*.35
		for y := max(0, int(surface)-3); y < min(p.hz, int(reach)+3); y++ {
			if p.nearer(m.depth, x, y) {
				continue
			}
			fy := float64(y) + .5
			n := fbm2(m.seed+97, float64(x)*.18, fy*.25-float64(p.frame)*.6, 3)
			if fy >= surface {
				a := smooth(reach+2, reach-2, fy) * (.75 + .25*n)
				p.blend(x, y, snow, a)
				p.blend(x, y, p.air[y*p.w+x], smooth(.65, 1, d)*a)
				continue
			}
			a := smooth(surface-3, surface, fy) * smooth(reach+2, reach-1, fy) * smooth(.3, .7, n)
			p.blend(x, y, powder, a*(1-smooth(.7, 1, d)))
		}
	}
}

// nearer reports whether a nearer ridgeline than depth covers pixel x, y.
func (p *painter) nearer(depth, x, y int) bool {
	for k := depth + 1; k < len(p.layers); k++ {
		if float64(y) >= float64(p.hz)-p.layers[k].hm[x] {
			return true
		}
	}
	return false
}
