// Package moai is the moai theme: an island coast at night or by day, each group a stone ahu and each unit a moai
// carved on it.
package moai

import (
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Moai draws each group as an ahu, a stone platform, and a chief as a ceremonial site holding several: each unit
// is a moai carved out of a block as it progresses, its agents the carvers, its pull requests stones hauled up to
// it, and its weather the sea and sky around it.
type Moai struct{}

func init() { scene.Register(Moai{}) }

func (Moai) Name() string { return "moai" }

func (Moai) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	l := plan(s, width, height)
	p := painter{c: scene.NewCanvas(width, height, box), bd: backdropOf(s, width, height, l.top+5), l: l, frame: frame, box: box, w: width, h: height}
	p.lt = p.bd.lt
	p.occ = make([]bool, width)
	p.ground()
	for i := range p.l.ahus {
		p.ahu(&p.l.ahus[i])
	}
	for _, f := range p.l.sites {
		p.bracket(f)
	}
	return p.c
}

func (Moai) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	l := plan(s, width, height)
	for _, a := range l.ahus {
		y0 := l.row(a.band)
		if y < y0 || y > y0+7 {
			continue
		}
		if y <= y0+4 {
			for _, q := range a.slots {
				if x >= q.x && x < q.x+q.w {
					return q.hit
				}
			}
			continue
		}
		if x >= a.x && x < a.x+a.w {
			return a.id
		}
	}
	for _, f := range l.sites {
		if y == l.row(f.band)+8 && x >= f.x0 && x < f.x1 {
			return f.id
		}
	}
	return ""
}

// light changes the sky once per bucket, so the backdrop is kept in between.
const lightStep = 10 * time.Minute

type backdropKey struct {
	w, h, horizon int
	dark          bool
	at            time.Time
}

// backdrop is the sky, hills and sea that hold still: each cell's colors and glyph, and where the sea starts.
type backdrop struct {
	key   backdropKey
	lt    light
	bg    []rgb
	fg    []rgb
	glyph []string
	sea   int
}

var backdrops struct {
	sync.Mutex
	last *backdrop
}

func backdropOf(s scene.Scene, width, height, horizon int) *backdrop {
	key := backdropKey{w: width, h: height, horizon: horizon, dark: s.Dark, at: s.Now.Truncate(lightStep)}
	backdrops.Lock()
	defer backdrops.Unlock()
	if b := backdrops.last; b != nil && b.key == key {
		return b
	}
	b := paintBackdrop(key)
	backdrops.last = b
	return b
}
