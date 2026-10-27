package mountains

import "math"

func (p *painter) terrain() {
	p.layers = make([]layer, p.l.depth+1)
	for d := range p.layers {
		p.layers[d] = layer{hm: make([]float64, p.w), own: make([]int, p.w)}
		for x := range p.w {
			p.layers[d].own[x] = -1
		}
	}
	for _, m := range p.l.massifs {
		lay, h := p.layers[m.depth], .44*p.layerHeight(m.depth)
		for x := max(0, int(m.x0)); x < min(p.w, int(math.Ceil(m.x1))); x++ {
			lay.hm[x] = math.Max(lay.hm[x], massifHeight(m, float64(x)+.5, h))
		}
	}
	p.geo = make([]geom, len(p.l.mounts))
	for i, m := range p.l.mounts {
		h := m.lift * p.layerHeight(m.depth)
		half := math.Max(m.half*1.4, h*.62)
		g := geom{height: h, half: half, x: m.cx}
		lay := p.layers[m.depth]
		top := 0.0
		for x := max(0, int(m.cx-half*1.3)); x < min(p.w, int(m.cx+half*1.3)+1); x++ {
			v := mountHeight(m, g, float64(x)+.5)
			if v > top {
				top, g.x = v, float64(x)+.5
			}
			if v > lay.hm[x] {
				lay.hm[x], lay.own[x] = v, i
			}
		}
		g.y = float64(p.hz) - top
		p.geo[i] = g
	}
	p.far = make([]float64, p.w)
	seed, h := uint64(0x5eed), .7*p.layerHeight(0)
	for x := range p.w {
		fx := float64(x)
		ridged := 1 - math.Abs(2*fbm(seed+1, fx*.045, 3)-1)
		p.far[x] = h * (.18 + .32*fbm(seed, fx*.02, 4) + .5*ridged*ridged)
	}
}

func massifHeight(m massif, x, h float64) float64 {
	w := m.x1 - m.x0
	edge := smooth(0, .2, (x-m.x0)/w) * smooth(0, .2, (m.x1-x)/w)
	return h * edge * (.6 + .6*fbm(m.seed, x*.05, 4) + .15*(fbm(m.seed+9, x*.6, 2)-.5))
}

// mountHeight is the silhouette of one mountain: a concave alpine pyramid with a shoulder and a broken ridge.
func mountHeight(m mount, g geom, x float64) float64 {
	t := (x - m.cx) / g.half
	if t < 0 {
		t *= 1 + (unit(m.seed, 5)-.5)*.6
	}
	u := math.Abs(t)
	if u >= 1 {
		return 0
	}
	base := .7*math.Pow(1-u, 1.8) + .3*(1-u)
	side := 1.0
	if unit(m.seed, 6) < .5 {
		side = -1
	}
	shoulder := .14 * math.Exp(-math.Pow((t-side*.45)/.13, 2))
	rough := (fbm(m.seed, x*.5, 3) - .5) * .3 * math.Sqrt(u) * (1 - u) * 2
	return g.height * math.Max(0, base+shoulder*(1-u)+rough)
}
