package pyramids

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

// standing is how many pixels over the unit's base visibly differ from the same scene without it.
func standing(t *testing.T, s scene.Scene) int {
	t.Helper()
	empty := scene.Scene{Dark: true, Ranges: []scene.Range{{ID: "repo", Name: "repo"}}}
	b := plan(s, 120, horizon(40)).ps[0]
	with, without := still(s, 120, 40).at(0, scene.Rect{}), still(empty, 120, 40).at(0, scene.Rect{})
	with.motion()
	defer with.release()
	n := 0
	for y := 0; y < b.base; y++ {
		for x := b.x0; x < b.x1; x++ {
			a, c := with.px[y*with.w+x], without.px[y*with.w+x]
			if math.Abs(a.R-c.R)+math.Abs(a.G-c.G)+math.Abs(a.B-c.B) > .06 {
				n++
			}
		}
	}
	return n
}

func TestPyramidRisesWithProgressAndCrumblesWhenKilled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lo, hi func(*scene.Peak)
	}{
		{"progress lays courses", func(p *scene.Peak) { p.Progress = .1 }, func(p *scene.Peak) { p.Progress = .9 }},
		{"a kill slides it into sand", func(p *scene.Peak) { p.Dying = .99 }, func(p *scene.Peak) {}},
		{"a kill sinks it as it plays", func(p *scene.Peak) { p.Dying = .9 }, func(p *scene.Peak) { p.Dying = .2 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if lo, hi := standing(t, lone(tc.lo)), standing(t, lone(tc.hi)); hi <= lo {
				t.Errorf("standing pixels %d then %d, want more", lo, hi)
			}
		})
	}
}

func TestCoursesFollowProgressAndNarrowToOneApexStone(t *testing.T) {
	prev := 0
	for _, f := range []float64{0, .25, .5, .75, 1} {
		b := plan(lone(func(p *scene.Peak) { p.Progress = f }), 120, horizon(40)).ps[0]
		if b.built < prev || b.built > b.planned {
			t.Errorf("progress %v: built %d of %d after %d", f, b.built, b.planned, prev)
		}
		prev = b.built
		if lo, hi := b.span(0); lo != b.x0 || hi != b.x1-1 {
			t.Errorf("base spans %d..%d, want %d..%d", lo, hi, b.x0, b.x1-1)
		}
		if lo, hi := b.span(b.planned - 1); lo != hi {
			t.Errorf("apex spans %d..%d, want one stone", lo, hi)
		}
	}
	if b := plan(lone(func(p *scene.Peak) { p.Weather = scene.Summit; p.Progress = .4 }), 120, horizon(40)).ps[0]; b.built != b.planned {
		t.Errorf("done: built %d of %d", b.built, b.planned)
	}
}

func TestBlocksClimbTheRampWithTheirStage(t *testing.T) {
	rows := map[int]int{}
	for _, stage := range []int{0, 2, 4, 6} {
		b := plan(lone(func(p *scene.Peak) { p.Camps = []scene.Camp{{Label: "#1", Stage: stage}} }), 120, horizon(40)).ps[0]
		rows[stage] = b.blocks[0].row
	}
	if !(rows[0] > rows[2] && rows[2] > rows[4] && rows[4] > rows[6]) {
		t.Errorf("rows by stage %v, want higher (smaller) as the stage grows", rows)
	}
}
