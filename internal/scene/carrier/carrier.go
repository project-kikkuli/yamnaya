// Package carrier is the carrier theme: a side view of carriers at sea, one per group, with each unit a
// squadron on a flight deck.
package carrier

import (
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Carrier draws each group as an aircraft carrier and a chief as a fleet of them: each unit is a squadron on
// deck, its agents the deck crew, its pull requests planes moving up to the catapult, and its weather the sea and
// sky around it.
type Carrier struct{}

func init() { scene.Register(Carrier{}) }

func (Carrier) Name() string { return "carrier" }

func (Carrier) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	l := plan(s, width, height)
	p := painter{c: scene.NewCanvas(width, height, box), bd: backdropOf(s, width, height, l.top+4), l: l, frame: frame, box: box, w: width, h: height}
	p.lt = p.bd.lt
	p.occ = make([]bool, width)
	p.sea()
	for i := range p.l.ships {
		p.ship(&p.l.ships[i])
	}
	for _, f := range p.l.fleets {
		p.bracket(f)
	}
	return p.c
}

func (Carrier) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	l := plan(s, width, height)
	for _, sh := range l.ships {
		y0 := l.row(sh.band)
		if y < y0 || y > y0+4 || x < sh.x || x >= sh.x+sh.w {
			continue
		}
		for _, q := range sh.squads {
			if y <= y0+2 && x >= q.x && x < q.x+q.w {
				return q.hit
			}
		}
		return sh.id
	}
	for _, f := range l.fleets {
		if y == l.row(f.band)+5 && x >= f.x0 && x < f.x1 {
			return f.id
		}
	}
	return ""
}

// light changes the sky once per bucket, so the backdrop is kept in between.
const lightStep = 10 * time.Minute

type backdropKey struct {
	w, h, sea int
	dark      bool
	at        time.Time
}

// backdrop is the sky and sea that hold still: each cell's colors and glyph, and the sea's first row.
type backdrop struct {
	key    backdropKey
	lt     light
	bg, fg []rgb
	glyph  []string
	sea    int
}

var backdrops struct {
	sync.Mutex
	last *backdrop
}

func backdropOf(s scene.Scene, width, height, sea int) *backdrop {
	key := backdropKey{w: width, h: height, sea: sea, dark: s.Dark, at: s.Now.Truncate(lightStep)}
	backdrops.Lock()
	defer backdrops.Unlock()
	if b := backdrops.last; b != nil && b.key == key {
		return b
	}
	b := paintBackdrop(key)
	backdrops.last = b
	return b
}
