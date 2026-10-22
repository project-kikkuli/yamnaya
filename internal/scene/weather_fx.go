package scene

import "math"

func (p *painter) weather() {
	for i, m := range p.l.mounts {
		switch m.weather {
		case Clouds:
			p.cloudBank(i)
		case Storm:
			p.storm(i)
		}
	}
}

// cloudBank wraps a quiet peak's flanks in torn, slow cloud.
func (p *painter) cloudBank(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	mid, thick := g.y+g.height*.4, math.Max(2, g.height*.12)
	for y := max(0, int(mid-thick*3)); y < min(p.hz, int(mid+thick*3)); y++ {
		for x := max(0, int(g.x-g.half*1.4)); x < min(p.w, int(g.x+g.half*1.4)); x++ {
			if !p.paints(x) {
				continue
			}
			fx, fy := float64(x)+.5, float64(y)+.5
			n := fbm2(m.seed+21, fx*.06, fy*.5, 5)
			tilt := mid + (fx-g.x)*.12 + (fbm(m.seed+22, fx*.05, 2)-.5)*thick*2
			band := math.Exp(-math.Pow((fy-tilt)/thick, 2)) * smooth(g.half*1.4, g.half*.5, math.Abs(fx-g.x))
			a := smooth(.25, .55, n*band*1.5)
			col := mix(p.th.cloud, p.th.cloudShade, smooth(tilt-thick, tilt+thick, fy))
			p.blend(x, y, col, a*.85)
		}
	}
}

// storm buries a stuck peak's summit in dark cloud.
func (p *painter) storm(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	rx, ry, cy := stormShape(g)
	type lump struct{ x, y, r float64 }
	var lumps []lump
	for k := range 7 {
		lumps = append(lumps, lump{g.x + (unit(m.seed, 200+k)-.5)*rx*1.8, cy - ry*.5 + (unit(m.seed, 210+k)-.6)*ry*1.2, ry * (.6 + .6*unit(m.seed, 220+k))})
	}
	for y := max(0, int(cy-ry*2.2)); y < min(p.hz, int(cy+ry*1.2)); y++ {
		for x := max(0, int(g.x-rx*1.8)); x < min(p.w, int(g.x+rx*1.8)); x++ {
			if !p.paints(x) {
				continue
			}
			fx, fy := float64(x)+.5, float64(y)+.5
			warp := fbm2(m.seed+33, fx*.08, fy*.15, 3) - .5
			e := 0.0
			for _, l := range lumps {
				d := math.Hypot((fx-l.x)/1.6+warp*4, fy-l.y+warp*3) / l.r
				e = math.Max(e, 1-d)
			}
			e *= smooth(cy+ry*1.1, cy+ry*.5, fy)
			n := fbm2(m.seed+31, fx*.12, fy*.3, 4)
			a := smooth(.0, .25, e+(n-.5)*.5)
			col := mix(p.th.stormTop, p.th.stormCloud, smooth(cy-ry*1.6, cy+ry*.2, fy)+(n-.5)*.6)
			p.blend(x, y, col, a*.97)
		}
	}
}

func stormShape(g geom) (rx, ry, cy float64) {
	rx, ry = math.Max(6, g.half*.75), math.Max(4, g.height*.32)
	return rx, ry, g.y + ry*.2
}

// bolt strikes now and then from a storm's cloud down onto the peak.
func (p *painter) bolt(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	if reducedMotion || (p.frame/4+int(m.seed%97))%13 != 0 {
		return
	}
	rx, ry, cy := stormShape(g)
	x, y := g.x+(unit(m.seed, p.frame/4)-.5)*rx, cy+ry*.4
	for step := 0; y < g.y+g.height*.55; step++ {
		p.set(int(x), int(y), p.th.bolt)
		x += (unit(m.seed+41, p.frame/4*31+step) - .5) * 2.4
		y++
	}
}

// lava runs in glowing threads from the crater of a peak whose manager died.
func (p *painter) lava(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	for k := range 3 {
		x := g.x + float64(k-1)*1.2
		dir := float64(k-1) * .55
		length := g.height * (.45 + .4*unit(m.seed, 50+k))
		for dy := 0.0; dy < length; dy++ {
			xx := int(math.Round(x + dir*dy*.5 + math.Sin(dy*.35+float64(k)*2)*1.1))
			yy := int(g.y + 1 + dy)
			if yy >= p.hz || xx < 0 || xx >= p.w || float64(p.hz)-p.layers[m.depth].hm[xx] > float64(yy) {
				continue
			}
			pulse := .5 + .5*math.Sin(dy*.45-p.t*.5+float64(k))
			col := mix(p.th.lavaDeep, mix(p.th.lava, p.th.lavaHot, pulse*pulse), (1-dy/length*.7)*(.6+.4*pulse))
			p.set(xx, yy, col)
			p.blend(xx-1, yy, p.th.lava, .35*(1-dy/length))
			p.blend(xx+1, yy, p.th.lava, .35*(1-dy/length))
		}
	}
}

// plume is the ash column over an eruption, billowing up and leaning downwind.
func (p *painter) plume(i int) {
	m, g := p.l.mounts[i], p.geo[i]
	life := clamp(g.height*2, 14, 80)
	puffs := int(clamp(g.half*2, 10, 44))
	for k := range puffs {
		age := math.Mod(p.t*.6+float64(k)*life/float64(puffs)+unit(m.seed, 60+k)*4, life)
		drift := age * age / life * .9
		fx := g.x + drift + (unit(m.seed, 120+k)-.5)*age*.35 + math.Sin(age*.15+float64(k))*1.5
		fy := g.y - 1 - age*.62
		r := (1.2 + age*.16) * (.7 + .6*unit(m.seed, 150+k))
		near := smooth(life*.3, 0, age)
		col := mix(p.th.smoke, p.th.smokeDark, .25+.55*unit(m.seed, 90+k))
		col = mix(col, p.th.lava, near*.75)
		fade := .85 * smooth(life, life*.5, age)
		for y := int(fy - r); y <= int(fy+r); y++ {
			for x := int(fx - r*1.3); x <= int(fx+r*1.3); x++ {
				d := math.Hypot((float64(x)+.5-fx)/1.3, float64(y)+.5-fy) / r
				if d < 1 {
					shade := mix(col, col.scale(.7), smooth(-.3, .8, (float64(y)-fy)/r))
					p.blend(x, y, shade, fade*smooth(1, .35, d))
				}
			}
		}
	}
}
