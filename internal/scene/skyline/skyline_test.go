package skyline

import (
	"math"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func lone(f func(*scene.Peak)) scene.Scene {
	p := scene.Peak{ID: "u", Name: "the unit", Progress: .5, Team: []scene.Climber{{Name: "w", Stance: scene.Climbing}}}
	f(&p)
	return scene.Scene{Dark: true, Ranges: []scene.Range{{ID: "repo", Name: "repo", Peaks: []scene.Peak{p}}}}
}

// standing is how many pixels of the unit's building column visibly differ from the same scene without it.
func standing(t *testing.T, s scene.Scene) int {
	t.Helper()
	empty := scene.Scene{Dark: true, Ranges: []scene.Range{{ID: "repo", Name: "repo"}}}
	b := plan(s, 120, horizon(40)).bs[0]
	with, without := still(s, 120, 40).at(0, scene.Rect{}), still(empty, 120, 40).at(0, scene.Rect{})
	with.motion()
	n := 0
	for y := 0; y < with.hz; y++ {
		for x := b.x0; x < b.x1; x++ {
			a, b := with.px[y*with.w+x], without.px[y*with.w+x]
			if math.Abs(a.R-b.R)+math.Abs(a.G-b.G)+math.Abs(a.B-b.B) > .06 {
				n++
			}
		}
	}
	return n
}

func TestBuildingRisesWithProgressAndIsGoneOnceDemolished(t *testing.T) {
	for _, tc := range []struct {
		name       string
		lo, hi     func(*scene.Peak)
		wantHigher bool
	}{
		{"progress builds floors", func(p *scene.Peak) { p.Progress = .1 }, func(p *scene.Peak) { p.Progress = .9 }, true},
		{"demolition brings it down", func(p *scene.Peak) { p.Dying = .99 }, func(p *scene.Peak) {}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lo, hi := standing(t, lone(tc.lo)), standing(t, lone(tc.hi))
			if (hi > lo) != tc.wantHigher {
				t.Errorf("standing pixels %d then %d", lo, hi)
			}
		})
	}
}

func TestBuiltFloorsFollowProgress(t *testing.T) {
	prev := 0
	for _, f := range []float64{0, .25, .5, .75, 1} {
		b := plan(lone(func(p *scene.Peak) { p.Progress = f }), 120, horizon(40)).bs[0]
		if b.built < prev || b.built > b.planned {
			t.Errorf("progress %v: built %d of %d after %d", f, b.built, b.planned, prev)
		}
		prev = b.built
	}
	if b := plan(lone(func(p *scene.Peak) { p.Progress = 1 }), 120, horizon(40)).bs[0]; b.built != b.planned {
		t.Errorf("done: built %d of %d", b.built, b.planned)
	}
}
