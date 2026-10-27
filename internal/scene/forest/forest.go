// Package forest is the forest theme: each group is a grove, or a chief's clearing, and each unit a tree that
// grows with its progress, with agents climbing the trunk and pull requests nesting up it.
package forest

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Forest draws each group as a grove of trees and each unit as a tree: its height is its progress, its busy
// agents are birds and squirrels on the trunk, and its pull requests are nests up it.
type Forest struct{}

func init() { scene.Register(Forest{}) }

func (Forest) Name() string { return "forest" }

func (Forest) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	p := still(s, width, height).at(frame, box)
	p.motion()
	defer p.release()
	return p.canvas()
}

func (Forest) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	p := still(s, width, height).at(0, box)
	p.motion()
	defer p.release()
	if i := p.who[y*p.w+x] - 1; i >= 0 {
		return p.l.ts[i].id
	}
	floor := p.hz / 2
	for k := len(p.l.gs) - 1; k >= 0; k-- {
		g := p.l.gs[k]
		if x >= g.x0 && x < g.x1 && y >= floor-g.depth-1 && y <= floor+1+g.depth {
			return g.id
		}
	}
	return ""
}

// light is how finely the time of day moves the light: once per bucket, so the backdrop is reused between.
const light = 10 * time.Minute

var backdrops struct {
	sync.Mutex
	key string
	p   *painter
}

// still is the backdrop for a scene at a size: sky, hills, the forest floor, the groves and clearings and every
// tree standing still in its weather, kept while nothing it paints changes. Trees that sway, burn or fall, the
// agents, nests, alerts and the selection go on top each frame.
func still(s scene.Scene, width, height int) *painter {
	s.Now = s.Now.Truncate(light)
	l := plan(s, width, horizon(height))
	key := shape(width, height, s, l)
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.p == nil || backdrops.key != key {
		p := blankPainter(paletteAt(s.Now, s.Dark), width, height)
		p.hour = hourOf(s.Now)
		p.l = l
		p.backdrop()
		backdrops.key, backdrops.p = key, p
	}
	q := *backdrops.p
	q.l = l
	return &q
}

// shape is everything the backdrop paints from: the size, the light, and where each tree and grove stands, how
// tall, in what weather; not names, agents, nests or the selection.
func shape(width, height int, s scene.Scene, l layout) string {
	var b strings.Builder
	fmt.Fprint(&b, width, " ", height, " ", s.Dark, " ", s.Now.Format(time.TimeOnly))
	for _, t := range l.ts {
		fmt.Fprintf(&b, "|%x %d %d %d %d %d %d %v", t.seed, t.cx, t.x0, t.x1, t.base, t.h, t.weather, t.dying > 0)
	}
	for _, g := range l.gs {
		fmt.Fprintf(&b, "|%x %d %d %d %v", g.seed, g.x0, g.x1, g.depth, g.chief)
	}
	return b.String()
}
