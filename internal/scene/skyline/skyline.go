package skyline

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Skyline draws each group as a district of a city and each unit as a building rising in it: its height is
// its progress, its busy agents are workers and a crane, and its pull requests are floors.
type Skyline struct{}

func init() { scene.Register(Skyline{}) }

func (Skyline) Name() string { return "skyline" }

func (Skyline) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	p := still(s, width, height).at(frame, box)
	p.motion()
	defer p.release()
	return p.canvas()
}

func (Skyline) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	p := still(s, width, height).at(0, box)
	p.motion()
	defer p.release()
	if i := p.who[y*p.w+x] - 1; i >= 0 {
		return p.l.bs[i].id
	}
	street := p.hz / 2
	for k := len(p.l.ds) - 1; k >= 0; k-- {
		d := p.l.ds[k]
		top, bottom := street-d.depth, street+1
		if d.depth > 0 {
			bottom = street - 1
		}
		if x >= d.x0 && x < d.x1 && y >= top && y <= bottom {
			return d.id
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

// still is the backdrop for a scene at a size: sky, street, water and every standing building in its weather,
// kept while nothing it paints changes. Workers, cranes, alerts, the selection and kills go on top each frame.
func still(s scene.Scene, width, height int) *painter {
	s.Now = s.Now.Truncate(light)
	l := plan(s, width, horizon(height))
	key := shape(width, height, s, l)
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.p == nil || backdrops.key != key {
		p := blankPainter(paletteAt(s.Now, s.Dark), width, height)
		p.l = l
		p.backdrop()
		backdrops.key, backdrops.p = key, p
	}
	q := *backdrops.p
	q.l = l
	return &q
}

// shape is everything the backdrop paints from: the size, the light, and where each building and district
// stands, how tall, in what weather and with which floors; not names, agents or the selection.
func shape(width, height int, s scene.Scene, l layout) string {
	var b strings.Builder
	fmt.Fprint(&b, width, " ", height, " ", s.Dark, " ", s.Now.Format(time.TimeOnly))
	for _, k := range l.bs {
		fmt.Fprintf(&b, "|%x %d %d %d %d %d %d %v %v", k.seed, k.x0, k.x1, k.base, k.planned, k.built, k.weather, k.dying > 0, k.floors)
	}
	for _, d := range l.ds {
		fmt.Fprintf(&b, "|%d %d %d", d.x0, d.x1, d.depth)
	}
	return b.String()
}
