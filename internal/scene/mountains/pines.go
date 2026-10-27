package mountains

import "math"

// pines frame the edges of a wide screen with tall snowy conifers in the foreground.
func (p *painter) pines() {
	if p.w < 110 {
		return
	}
	for k := range 5 {
		side := float64(k%2) * float64(p.w)
		u := unit(0x9145, k)
		x := math.Abs(side - float64(p.w)*(.01+.07*u))
		height := float64(p.p-p.hz) + float64(p.hz)*(.1+.22*unit(0x9146, k))
		p.pine(x, height, uint64(k))
	}
}

func (p *painter) pine(cx, height float64, seed uint64) {
	base := float64(p.p) + 1
	for y := int(base - height); y < p.p; y++ {
		s := float64(y) - (base - height)
		if y < 0 {
			continue
		}
		tier := math.Mod(s*.16+unit(seed, 1), 1)
		half := s * .2 * (.55 + .45*tier)
		if s < 2 {
			half = .5
		}
		for x := int(cx - half); x <= int(cx+half); x++ {
			if x < 0 || x >= p.w {
				continue
			}
			edge := math.Abs(float64(x)+.5-cx) / math.Max(.5, half)
			c := p.th.tree
			if tier < .22 && edge > .25 && unit(seed+7, x*131+y) < .7 {
				c = mix(p.th.tree, p.th.forestSnow, .55)
			}
			p.set(x, y, c)
			p.solid[y*p.w+x] = true
			p.front[y*p.w+x] = true
		}
	}
}
