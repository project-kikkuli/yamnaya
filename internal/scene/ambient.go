package scene

import (
	"math"
	"time"
)

// Ambient is how often the scene's own life moves: stars, drifting cloud and light on the water. Each change
// touches only some rows, so the terminal redraws little.
const Ambient = 2500 * time.Millisecond

// ambient takes turns: on one beat the stars twinkle and the high cloud drifts, on the next the lake shimmers.
func (p *painter) ambient() {
	beat := p.frame / int(Ambient/Step)
	sky, water := beat/2, (beat+1)/2
	p.stars(sky)
	p.drift(sky)
	p.shimmer(water)
}

func (p *painter) stars(beat int) {
	if p.th.stars <= 0 {
		return
	}
	for y := range int(float64(p.hz) * .62) {
		v := float64(y) / float64(p.hz)
		for x := range p.w {
			i := y*p.w + x
			if p.solid[i] || spread(0x57a5, i)%1000 >= 5 {
				continue
			}
			b := (.35 + .65*unit(0x57a6, i)) * (.45 + .55*unit(0x57a7, i*31+beat))
			p.px[i] = mix(p.px[i], white, b*p.th.stars*(1-v/.62))
		}
	}
}

// drift moves a few long wisps of high cloud across the open sky, behind the peaks.
func (p *painter) drift(beat int) {
	col := mix(p.th.cloudShade, p.th.cloud, .7)
	for k := range 4 {
		span := float64(p.w + 60)
		cx := math.Mod(unit(0xd41f, k)*span+float64(beat)*(1.5+unit(0xd420, k)), span) - 30
		cy := float64(p.hz) * (.15 + .35*unit(0xd421, k))
		rx, ry := 14+16*unit(0xd422, k), 1.2+unit(0xd423, k)
		for y := max(0, int(cy-ry*2)); y < min(p.hz, int(cy+ry*2)+1); y++ {
			for x := max(0, int(cx-rx)); x < min(p.w, int(cx+rx)+1); x++ {
				i := y*p.w + x
				if p.solid[i] {
					continue
				}
				d := math.Hypot((float64(x)-cx)/rx, (float64(y)-cy)/ry)
				p.px[i] = mix(p.px[i], col, .4*smooth(1, .2, d))
			}
		}
	}
}

// shimmer is light moving on the lake: glints of the horizon sky and dark troughs that shift with the wind.
func (p *painter) shimmer(beat int) {
	glint := p.th.skyAt(.97)
	for y := p.hz; y < p.p; y++ {
		k := float64(y - p.hz)
		for x := range p.w {
			i := y*p.w + x
			if p.front[i] {
				continue
			}
			swell := noise2(0x1a4e, float64(x)*.07+float64(beat)*.9, k*.75)
			switch {
			case swell > .7:
				p.px[i] = mix(p.px[i], glint, (swell-.7)*1.6)
			case swell < .28:
				p.px[i] = p.px[i].scale(.84)
			}
		}
	}
}
