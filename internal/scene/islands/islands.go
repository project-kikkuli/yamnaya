package islands

import (
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Islands draws the board from above: each group an archipelago, each unit an island with its boats sailing
// round it and its pull requests built on it, and its weather over it.
type Islands struct{}

func init() { scene.Register(Islands{}) }

func (Islands) Name() string { return "islands" }

func (Islands) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	p := still(s, width, height).at(frame, box)
	p.motion()
	defer p.release()
	return p.canvas()
}

func (Islands) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	p := still(s, width, height).at(0, box)
	p.motion()
	defer p.release()
	switch w := p.who[y*p.w+x]; {
	case w > 0:
		return p.l.isles[w-1].id
	case w < 0:
		return p.l.groups[-w-1].id
	}
	fx, fy := float64(x)+.5, float64(2*y)+1
	for _, is := range p.l.isles {
		if math.Hypot(fx-is.cx, fy-is.cy) <= is.r*1.35 {
			return is.id
		}
	}
	for g := len(p.l.groups) - 1; g >= 0; g-- {
		if p.l.groups[g].area.has(fx, fy) {
			return p.l.groups[g].id
		}
	}
	return ""
}

// light is how finely the time of day moves the light: once per bucket, so the backdrop is reused in between.
const light = 10 * time.Minute

var backdrops struct {
	sync.Mutex
	key string
	b   *backdrop
}

// sea is how far down the screen the archipelagos reach, as a share of its height; open water lies below.
const sea = .66

// still is the backdrop for a scene at a size, with the current layout to draw the frame's work on. The
// backdrop is kept while nothing it paints changes: the islands' places, sizes, greenery and weather, the
// lagoons, and the time of day to the bucket. The table plays no part in it.
func still(s scene.Scene, width, height int) *painter {
	now := s.Now.Truncate(light)
	l := plan(s, width, 3, float64(2*height)*sea)
	key := shape(width, height, s.Dark, now, l)
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.b == nil || backdrops.key != key {
		backdrops.key, backdrops.b = key, build(paletteAt(now, s.Dark), l, width, height)
	}
	return &painter{backdrop: backdrops.b, l: l}
}

// shape is everything about a layout the backdrop paints; not names, boats or buildings, which each frame draws.
func shape(width, height int, dark bool, now time.Time, l layout) string {
	b := make([]byte, 0, 64+48*len(l.isles))
	b = strconv.AppendInt(b, int64(width), 10)
	b = append(b, ' ')
	b = strconv.AppendInt(b, int64(height), 10)
	b = strconv.AppendBool(append(b, ' '), dark)
	b = now.AppendFormat(append(b, ' '), time.TimeOnly)
	f := func(v float64) { b = strconv.AppendFloat(append(b, ' '), v, 'g', 6, 64) }
	for _, is := range l.isles {
		b = strconv.AppendUint(append(b, '|'), is.seed, 16)
		f(is.cx)
		f(is.cy)
		f(is.r)
		f(is.progress)
		b = strconv.AppendInt(append(b, ' '), int64(is.weather), 10)
	}
	for _, g := range l.groups {
		b = strconv.AppendUint(append(b, '/'), g.seed, 16)
		f(g.lx)
		f(g.ly)
		f(g.la)
		f(g.lb)
	}
	return string(b)
}
