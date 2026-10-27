package anthill

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func lone(f func(*scene.Peak)) scene.Scene {
	p := scene.Peak{ID: "u", Name: "the unit", Progress: .5, Team: []scene.Climber{{Name: "w", Stance: scene.Climbing}}}
	f(&p)
	return scene.Scene{Dark: true, Ranges: []scene.Range{{ID: "repo", Name: "repo", Peaks: []scene.Peak{p}}}}
}

// count is how many pixels of the unit's column of the cutaway, above the surface or below it, are mound or
// open tunnel in frame 0.
func count(s scene.Scene, above bool) int {
	p := still(s, 120, 40).at(0, scene.Rect{})
	defer p.release()
	p.motion()
	m := p.l.ms[0]
	n := 0
	for y := range p.p {
		for x := m.x0 - 4; x < m.x1+4; x++ {
			i := y*p.w + x
			switch {
			case above && y < p.g.sy-2 && p.solid[i]:
				n++
			case !above && y > p.g.sy && y < p.g.gallery(0) && !p.solid[i]:
				n++
			}
		}
	}
	return n
}

func TestMoundAndNestGrowWithProgressAndAreGoneOnceFlattened(t *testing.T) {
	for _, tc := range []struct {
		name   string
		above  bool
		lo, hi func(*scene.Peak)
	}{
		{"progress raises the mound", true, func(p *scene.Peak) { p.Progress = .1 }, func(p *scene.Peak) { p.Progress = .9 }},
		{"progress spreads the nest", false, func(p *scene.Peak) { p.Progress = .1 }, func(p *scene.Peak) { p.Progress = .9 }},
		{"a kill flattens the mound", true, func(p *scene.Peak) { p.Dying = .99 }, func(p *scene.Peak) {}},
		{"a kill caves the tunnels in", false, func(p *scene.Peak) { p.Dying = .99 }, func(p *scene.Peak) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if lo, hi := count(lone(tc.lo), tc.above), count(lone(tc.hi), tc.above); hi <= lo*2 {
				t.Errorf("%d pixels, then %d", lo, hi)
			}
		})
	}
}

func TestChambersLieDeeperTheMoreGatesTheirPullRequestPassed(t *testing.T) {
	prev := 0
	for stage := range 7 {
		m := plan(lone(func(p *scene.Peak) { p.Camps = []scene.Camp{{Label: "#1", Stage: stage}} }), 120, geometry(40)).ms[0]
		if y := m.chambers[0].y; y < prev || y >= m.bottom {
			t.Errorf("stage %d: chamber at row %d after %d, shaft ends at %d", stage, y, prev, m.bottom)
		} else {
			prev = y
		}
	}
	if first := plan(lone(func(p *scene.Peak) { p.Camps = []scene.Camp{{Stage: 0}} }), 120, geometry(40)).ms[0].chambers[0].y; prev <= first {
		t.Errorf("a merged-stage chamber at row %d is no deeper than a new one at %d", prev, first)
	}
}
