// Package pyramids is the pyramids theme: each group a necropolis on the desert by the Nile, each unit a
// pyramid whose stone courses rise with its progress, workers hauling up its ramps.
package pyramids

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Pyramids draws each group as a necropolis and each unit as a pyramid rising in it: its courses are its
// progress, its busy agents haul sledges up the west ramp, and its pull requests are blocks on the east ramp.
type Pyramids struct{}

func init() { scene.Register(Pyramids{}) }

func (Pyramids) Name() string { return "pyramids" }

func (Pyramids) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	p := still(s, width, height).at(frame, box)
	p.motion()
	defer p.release()
	return p.canvas()
}

func (Pyramids) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	p := still(s, width, height).at(0, box)
	p.motion()
	defer p.release()
	if i := p.who[y*p.w+x] - 1; i >= 0 {
		return p.l.ps[i].id
	}
	for k := len(p.l.ns) - 1; k >= 0; k-- {
		n := p.l.ns[k]
		ground := (p.hz - 2*n.depth) / 2
		if x >= n.x0 && x < n.x1 && y >= ground-2 && y <= p.hz/2+1 {
			return n.id
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

// still is the backdrop for a scene at a size: sky, dunes, sand, the Nile and every standing pyramid in its
// weather, kept while nothing it paints changes. Workers, blocks, alerts, the selection and kills go on top.
func still(s scene.Scene, width, height int) *painter {
	s.Now = s.Now.Truncate(light)
	l := plan(s, width, horizon(height))
	key := shape(width, height, s, l)
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.p == nil || backdrops.key != key {
		p := blankPainter(paletteAt(s.Now, s.Dark), width, height, hourOf(s.Now))
		p.l = l
		p.backdrop()
		backdrops.key, backdrops.p = key, p
	}
	q := *backdrops.p
	q.l = l
	return &q
}

// shape is everything the backdrop paints from: the size, the light, and where each pyramid and necropolis
// stands, how tall, in what weather and with which courses cased; not names, agents or the selection.
func shape(width, height int, s scene.Scene, l layout) string {
	var b strings.Builder
	fmt.Fprint(&b, width, " ", height, " ", s.Dark, " ", s.Now.Format(time.TimeOnly))
	for _, k := range l.ps {
		fmt.Fprintf(&b, "|%x %d %d %d %d %d %d %v %v %v", k.seed, k.x0, k.x1, k.base, k.planned, k.built, k.weather, k.dying > 0, k.cased, k.hauling())
	}
	for _, n := range l.ns {
		fmt.Fprintf(&b, "|%d %d %d %v", n.x0, n.x1, n.depth, n.chief)
	}
	return b.String()
}

// hourOf is the local hour a time stands for; zero is dusk.
func hourOf(now time.Time) float64 {
	if now.IsZero() {
		return 19.5
	}
	return float64(now.Hour()) + float64(now.Minute())/60 + float64(now.Second())/3600
}
