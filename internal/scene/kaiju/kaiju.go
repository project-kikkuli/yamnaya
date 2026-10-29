package kaiju

import (
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Kaiju draws the board as a monster picture: each group a district of a harbor city, each unit one of a roster of
// monsters wading in from the bay, the defenders' jets, tanks and searchlights round it, its weather on it.
type Kaiju struct{}

func init() { scene.Register(Kaiju{}) }

func (Kaiju) Name() string { return "kaiju" }

func (Kaiju) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	p := still(s, width, height).at(frame, box)
	defer p.release()
	p.draw()
	return p.c
}

func (Kaiju) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	p := still(s, width, height).at(0, box)
	defer p.release()
	p.draw()
	if w := p.who[y*p.w+x]; w > 0 {
		return p.l.monsters[w-1].id
	} else if w < 0 {
		return p.l.districts[-w-1].id
	}
	if g := p.l.group(float64(x) + .5); g >= 0 {
		return p.l.districts[g].id
	}
	return ""
}

// draw shoots the frame: lights and monsters on the set, the film's print of it, then the glyphs on top.
func (p *painter) draw() {
	for k, ph := range p.phase {
		if ph != noSwell {
			p.lum[k] *= swell[(int(ph)+p.frame)&31]
		}
	}
	for _, k := range p.glint {
		if unit(0x61a, int(k)+p.frame/2*7919) < .15 {
			p.lum[k] = float32(p.pal.moon * .6)
		}
	}
	p.searchlights()
	for i := range p.l.monsters {
		p.fixed(i)
	}
	for lane := 0; lane < 2; lane++ {
		for i := range p.l.monsters {
			if p.l.monsters[i].lane == lane {
				p.monster(i)
				p.weather(i)
			}
		}
	}
	for i := range p.l.monsters {
		p.fires(i)
	}
	p.eggs()
	p.film()
	p.canvasOf()
	p.sprites()
}

// swell is how a wave's pass lifts the bay's light, over the 32 steps of its phase.
var swell = func() (out [32]float32) {
	for i := range out {
		out[i] = float32(1 + .14*math.Sin(2*math.Pi*float64(i)/32))
	}
	return out
}()

// light is how finely the time of day moves the stock: once per bucket, so the backdrop is reused in between.
const light = 10 * time.Minute

var backdrops struct {
	sync.Mutex
	key string
	b   *backdrop
}

// still is the backdrop for a scene at a size, with the current layout for the frame to draw on. The backdrop
// is kept while nothing it paints changes: the size, the districts, and the time of day to the bucket.
func still(s scene.Scene, width, height int) *backdrop {
	now := s.Now.Truncate(light)
	l := plan(s, width, height)
	key := shape(width, height, s.Dark, now, l)
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.b == nil || backdrops.key != key {
		backdrops.key, backdrops.b = key, build(paletteAt(now, s.Dark), l, width, height)
	}
	b := *backdrops.b
	b.l = l
	return &b
}

func shape(width, height int, dark bool, now time.Time, l layout) string {
	b := make([]byte, 0, 64+24*len(l.districts))
	b = strconv.AppendInt(b, int64(width), 10)
	b = strconv.AppendInt(append(b, ' '), int64(height), 10)
	b = strconv.AppendBool(append(b, ' '), dark)
	b = now.AppendFormat(append(b, ' '), time.TimeOnly)
	for _, d := range l.districts {
		b = strconv.AppendUint(append(b, '|'), d.seed, 16)
		b = strconv.AppendFloat(append(b, ' '), d.x0, 'g', 6, 64)
		b = strconv.AppendFloat(append(b, ' '), d.x1, 'g', 6, 64)
	}
	return string(b)
}
