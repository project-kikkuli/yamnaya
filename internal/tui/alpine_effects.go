package tui

import "math"

func (c *alpineCanvas) effect(e alpineEvent, x, peak int, frame uint64) {
	age := float64(frame - e.frame)
	end := c.h - 3
	switch e.kind {
	case "storm":
		strength := math.Min(1, age/9) * math.Min(1, (75-age)/16)
		for y := 0; y <= end; y++ {
			level := float64(end-y) / float64(max(1, end))
			radius := int((1 + level*7) * strength)
			center := x + int(math.Sin(age*0.23+float64(y)*0.8)*(1+level*2))
			for dx := -radius; dx <= radius; dx++ {
				r := []rune("⠶⣦⣤⣀⣠⣴⠶")[(dx+radius+int(age/2)+y)%7]
				ink := alpineStorm
				if (dx+int(age/2)+y)%4 == 0 {
					ink = alpineSnow
				}
				c.put(center+dx, y, r, ink)
			}
		}
		for j := range min(18, e.people*2+4) {
			phase := age*0.22 + float64(j)*2.4
			radius := (3 + float64(j%5)) * strength
			xx := x + int(math.Cos(phase)*radius*1.5)
			y := end - int(math.Mod(age*0.18+float64(j)*1.7, float64(max(1, end+1))))
			r := []rune{'o', 'λ', '╱', '⚑', '╲'}[j%5]
			c.put(xx, y, r, alpineGold)
		}
	case "summit":
		c.put(x, peak, '⚑', alpineGold)
		for j := range 12 {
			angle := float64(j) * math.Pi / 6
			radius := age / 8
			xx := x + int(math.Cos(angle)*radius*2)
			y := peak - 1 + int(math.Sin(angle)*radius*0.6)
			c.put(xx, y, []rune{'✦', '·', '⠂'}[min(2, int(age/15))], alpineGlow)
		}
	case "depart":
		x -= 5
		for j := range 5 {
			c.put(x+j, end-j/2, '·', alpineGold)
		}
		c.put(x+int(age/10), max(0, end-int(age/20)), '⚑', alpineGold)
	case "work", "fall":
		for j := range 5 {
			xx := x + int(age/9) + j
			y := peak + int(age/12) + j/3
			if y <= end {
				c.put(xx, y, []rune{'⠁', '⠂', '⠄'}[(int(age/3)+j)%3], alpineIce)
			}
		}
	}
}
