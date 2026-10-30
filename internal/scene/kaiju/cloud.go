package kaiju

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type cloudShape struct {
	cx, cy, r float64
	storm     bool
}

type cloudPixel struct {
	k              int
	x, y, envelope float64
}

func stormShape(m *monster, top float64) cloudShape {
	return cloudShape{m.x, math.Max(top+2, m.top()-m.h*.15), m.span()*1.2 + 4, true}
}

func (b *backdrop) cloudsFor(l layout) map[cloudShape][]cloudPixel {
	keys := make([]cloudShape, 0, len(l.monsters))
	for i := range l.monsters {
		m := &l.monsters[i]
		if m.dying > 0 {
			continue
		}
		switch m.weather {
		case scene.Clouds:
			keys = append(keys, cloudShape{m.x, m.top() + m.h*.35, m.span()*1.1 + 3, false})
		case scene.Storm:
			keys = append(keys, stormShape(m, l.top))
		case scene.Eruption:
			mx, my := m.mouth()
			keys = append(keys, cloudShape{mx + m.facing*(m.h*.6+6), math.Max(my+4, l.ground-1) - 6, 6, false})
		}
	}
	same := len(keys) == len(b.clouds)
	for _, key := range keys {
		if _, ok := b.clouds[key]; !ok {
			same = false
		}
	}
	if same {
		return b.clouds
	}
	clouds := make(map[cloudShape][]cloudPixel, len(keys))
	for _, key := range keys {
		if pixels, ok := b.clouds[key]; ok {
			clouds[key] = pixels
		} else {
			clouds[key] = b.cloud(key)
		}
	}
	return clouds
}

func (b *backdrop) cloud(c cloudShape) []cloudPixel {
	ry, end, inner := c.r*.7, c.r*.7, .45
	if c.storm {
		ry, end, inner = c.r*.45, c.r*.3, .4
	}
	var pixels []cloudPixel
	for y := max(int(b.l.top), int(c.cy-ry)); y <= min(int(b.l.bottom)-1, int(c.cy+end)); y++ {
		for x := max(0, int(c.cx-c.r)); x <= min(b.w-1, int(c.cx+c.r)); x++ {
			fx, fy := float64(x)+.5, float64(y)+.5
			env := smooth(1, inner, math.Hypot((fx-c.cx)/c.r, (fy-c.cy)/ry))
			if env > 0 {
				pixels = append(pixels, cloudPixel{y*b.w + x, fx, fy, env})
			}
		}
	}
	return pixels
}
