// Package anthill draws the board as a cutaway of an ant colony: mounds on the surface and their nests, chambers
// and galleries underground.
package anthill

import (
	"math"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Anthill draws each group as a colony and each unit as a mound over its nest: progress raises the mound and
// spreads the nest, busy agents are ants marching the tunnels, and pull requests are chambers off the shaft.
type Anthill struct{}

func init() { scene.Register(Anthill{}) }

func (Anthill) Name() string { return "anthill" }

func (Anthill) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	p := still(s, width, height).at(frame, box)
	p.motion()
	defer p.release()
	return p.canvas()
}

func (Anthill) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	p := still(s, width, height).at(0, box)
	p.motion()
	defer p.release()
	if i := p.who[y*p.w+x] - 1; i >= 0 {
		return p.l.ms[i].id
	}
	for k := len(p.l.cs) - 1; k >= 0; k-- {
		c := p.l.cs[k]
		if row := p.g.gallery(c.depth) / 2; x >= c.x0 && x < c.x1 && y >= row-1 && y <= row+1 {
			return c.id
		}
	}
	return ""
}

// light is how finely the time of day moves the light: once per bucket, so the backdrop is reused between.
const light = 10 * time.Minute

var backdrops struct {
	sync.Mutex
	key uint64
	p   *painter
}

// still is the backdrop for a scene at a size: sky, soil, galleries and every standing mound with its nest and
// chambers, kept while nothing it paints changes. Ants, weather, alarms, labels and kills go on top each frame.
func still(s scene.Scene, width, height int) *painter {
	s.Now = s.Now.Truncate(light)
	g := geometry(height)
	l := plan(s, width, g)
	key := shape(width, height, s, l)
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.p == nil || backdrops.key != key {
		p := blankPainter(paletteAt(s.Now, s.Dark), hourOf(s.Now), width, height)
		p.l = l
		p.backdrop()
		backdrops.key, backdrops.p = key, p
	}
	q := *backdrops.p
	q.l = l
	return &q
}

// shape is a hash of everything the backdrop paints from: the size, the light, and where each mound and colony
// lies, how grown, and with which chambers; not names, agents, weather or the selection.
func shape(width, height int, s scene.Scene, l layout) uint64 {
	h := uint64(14695981039346656037)
	add := func(v int) { h = (h ^ uint64(v)) * 1099511628211 }
	add(width)
	add(height)
	if s.Dark {
		add(1)
	}
	add(int(s.Now.UnixNano() / int64(time.Minute)))
	add(int(math.Round(hourOf(s.Now) * 60)))
	for _, m := range l.ms {
		add(int(m.seed))
		for _, v := range [...]int{m.x0, m.x1, m.height, m.bottom, int(m.grown() * 1000)} {
			add(v)
		}
		if m.dying > 0 {
			add(-1)
		}
		for _, c := range m.chambers {
			add(c.y*8 + c.side*4 + int(c.state))
		}
	}
	for _, c := range l.cs {
		for _, v := range [...]int{c.gx0, c.gx1, c.link, c.depth, c.parent} {
			add(v)
		}
	}
	return h
}
