// Package civilizations is the civilizations theme: a strategy-game world map seen from above, each group a
// continent and each unit a civilization whose territory spreads as its work does.
package civilizations

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// Civilizations draws each group as a continent and a chief as an empire's dashed border round its continents:
// each unit is a civilization, its agents units on its road, its pull requests cities, and its weather what
// befalls it.
type Civilizations struct{}

func init() { scene.Register(Civilizations{}) }

func (Civilizations) Name() string { return "civilizations" }

// lightStep is how long the light holds before the map is lit again.
const lightStep = 10 * time.Minute

func (Civilizations) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	c := scene.NewCanvas(width, height, box)
	stillOf(s, width, height).frame(c, frame, box)
	return c
}

func (Civilizations) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	l := plan(s, width, height)
	for _, n := range l.lands {
		r0 := l.row(n.band)
		if y < r0 || y > r0+5 {
			continue
		}
		if y > r0 {
			for _, c := range n.civs {
				if x >= c.x && x < c.x+c.w {
					return c.hit
				}
			}
		}
		if n.id != "" && x >= n.x-1 && x <= n.x+n.w {
			return n.id
		}
	}
	for _, e := range l.empires {
		r0 := l.row(e.band)
		if y >= r0 && y <= r0+6 && x >= e.x0 && x <= e.x1 {
			return e.id
		}
	}
	return ""
}
