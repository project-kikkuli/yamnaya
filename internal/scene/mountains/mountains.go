package mountains

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Mountains draws each group as a range and each unit as a peak: its agents climb the west face, its pull
// requests are camps up the east face, and its weather is the sky over it.
type Mountains struct{}

func init() { scene.Register(Mountains{}) }

func (Mountains) Name() string { return "mountains" }

func (Mountains) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	p := still(s, width, height).at(frame, box)
	p.motion()
	defer p.release()
	return p.canvas()
}

// light is how finely the time of day moves the sky: once per bucket, so the backdrop is reused in between.
const light = 10 * time.Minute

var backdrops struct {
	sync.Mutex
	key    string
	p      *painter
	builds int
	// lit holds, per mountain, the pixels its selection light changes on the backdrop.
	lit map[int][]pixel
}

// still is the backdrop for a scene at a size with the selected mountain lit, the work and selection laid out
// on it for the frame to draw. The backdrop is kept while nothing it paints changes: the shape of the ranges,
// their weather, the theme and the time of day to the bucket. The table plays no part in it.
func still(s scene.Scene, width, height int) *painter {
	s.Now = s.Now.Truncate(light)
	l := plan(s, width)
	bare := unlit(l)
	key := fmt.Sprintf("%d %d %v %v %v", width, height, s.Dark, s.Now.Format(time.TimeOnly), shape(bare))
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.p == nil || backdrops.key != key {
		backdrops.builds++
		p := blankPainter(s, width, height, 0)
		p.l = bare
		p.backdrop()
		backdrops.key, backdrops.p, backdrops.lit = key, p, map[int][]pixel{}
		go warm(key, p, bare)
	}
	q := *backdrops.p
	q.l = l
	for i, m := range l.mounts {
		if !m.selected {
			continue
		}
		lit, ok := backdrops.lit[i]
		if !ok {
			lit = lighting(backdrops.p, bare, i)
			backdrops.lit[i] = lit
		}
		q.lit = lit
	}
	return &q
}

// warm works out every mountain's selection light on base, the backdrop of bare, ahead of the keys that move
// the selection to it.
func warm(key string, base *painter, bare layout) {
	for i := range bare.mounts {
		backdrops.Lock()
		_, done := backdrops.lit[i]
		stale := backdrops.key != key
		backdrops.Unlock()
		if stale {
			return
		}
		if done {
			continue
		}
		lit := lighting(base, bare, i)
		backdrops.Lock()
		if backdrops.key == key {
			backdrops.lit[i] = lit
		}
		backdrops.Unlock()
	}
}

// lighting is the pixels that differ from the bare backdrop when mountain i is the selected one. Only the columns
// it stands in change, and the lake's ripples reach two columns further, so only those are painted again.
func lighting(base *painter, bare layout, i int) []pixel {
	q := *base
	q.l = bare
	q.l.mounts = append([]mount(nil), bare.mounts...)
	q.l.mounts[i].selected = true
	q.px = append([]rgb(nil), base.px...)
	q.solid = append([]bool(nil), base.solid...)
	q.front = append([]bool(nil), base.front...)
	q.only = make([]bool, q.w)
	for _, lay := range q.layers {
		for x, own := range lay.own {
			if own == i {
				for k := max(0, x-2); k <= min(q.w-1, x+2); k++ {
					q.only[k] = true
				}
			}
		}
	}
	for x, on := range q.only {
		if on {
			for y := range q.p {
				q.px[y*q.w+x] = base.pre[y*q.w+x]
			}
		}
	}
	q.landscape()
	var out []pixel
	for k, c := range q.px {
		if c != base.px[k] {
			out = append(out, pixel{k, c})
		}
	}
	return out
}

// unlit is a layout with no mountain selected, the way the backdrop is painted.
func unlit(l layout) layout {
	l.mounts = append([]mount(nil), l.mounts...)
	for i := range l.mounts {
		l.mounts[i].selected = false
	}
	return l
}

// shape is everything about a layout the backdrop paints: where each mountain and range stands, how tall and
// in what weather; not its names, climbers or camps, which are drawn on top each frame.
func shape(l layout) string {
	var b strings.Builder
	fmt.Fprint(&b, l.depth)
	for _, m := range l.mounts {
		fmt.Fprintf(&b, "|%x %v %v %v %d %d", m.seed, m.cx, m.half, m.lift, m.depth, m.weather)
	}
	for _, m := range l.massifs {
		fmt.Fprintf(&b, "|%x %v %v %d %d", m.seed, m.x0, m.x1, m.depth, m.weather)
	}
	return b.String()
}

func (Mountains) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	p := still(s, width, height).at(0, box)
	p.motion()
	defer p.release()
	if i := p.who[y*p.w+x] - 1; i >= 0 {
		return p.l.mounts[i].id
	}
	for d := len(p.layers) - 1; d >= 0; d-- {
		lay := p.layers[d]
		if float64(2*y+1) < float64(p.hz)-lay.hm[x] || 2*y >= p.hz {
			continue
		}
		if i := lay.own[x]; i >= 0 {
			return p.l.mounts[i].id
		}
		for _, m := range p.l.massifs {
			if m.depth == d && float64(x) >= m.x0 && float64(x) < m.x1 {
				return m.id
			}
		}
	}
	return ""
}
