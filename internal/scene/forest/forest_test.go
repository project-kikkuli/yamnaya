package forest

import (
	"math"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func lone(f func(*scene.Peak)) scene.Scene {
	p := scene.Peak{ID: "u", Name: "the unit", Progress: .5, Team: []scene.Climber{{Name: "w", Stance: scene.Climbing}}}
	f(&p)
	return scene.Scene{Dark: true, Ranges: []scene.Range{{ID: "repo:r", Name: "repo", Peaks: []scene.Peak{p}}}}
}

// standing is how many pixels above the forest floor visibly differ from the same scene without the unit.
func standing(t *testing.T, s scene.Scene) int {
	t.Helper()
	empty := scene.Scene{Dark: true, Ranges: []scene.Range{{ID: "repo:r", Name: "repo"}}}
	with, without := still(s, 120, 40).at(0, scene.Rect{}), still(empty, 120, 40).at(0, scene.Rect{})
	with.motion()
	without.motion()
	defer with.release()
	defer without.release()
	n := 0
	for y := 0; y < with.hz-2; y++ {
		for x := range with.w {
			a, b := with.px[y*with.w+x], without.px[y*with.w+x]
			if math.Abs(a.R-b.R)+math.Abs(a.G-b.G)+math.Abs(a.B-b.B) > .06 {
				n++
			}
		}
	}
	return n
}

func TestTreeGrowsWithProgressAndIsGoneOnceFelled(t *testing.T) {
	for _, tc := range []struct {
		name   string
		lo, hi func(*scene.Peak)
	}{
		{"progress grows the tree", func(p *scene.Peak) { p.Progress = .1 }, func(p *scene.Peak) { p.Progress = .9 }},
		{"felling brings it down", func(p *scene.Peak) { p.Dying = .99 }, func(p *scene.Peak) {}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if lo, hi := standing(t, lone(tc.lo)), standing(t, lone(tc.hi)); hi <= lo {
				t.Errorf("standing pixels %d then %d", lo, hi)
			}
		})
	}
}

func TestNestsClimbTheTrunkWithTheirStage(t *testing.T) {
	prev := 1 << 30
	for stage := range 7 {
		tr := plan(lone(func(p *scene.Peak) { p.Camps = []scene.Camp{{Label: "#1", Stage: stage}} }), 120, horizon(40)).ts[0]
		if row := tr.nests[0].row; row > prev || row < tr.top() || row > tr.ground() {
			t.Errorf("stage %d: nest at row %d after %d, trunk rows %d..%d", stage, row, prev, tr.top(), tr.ground())
		} else {
			prev = row
		}
	}
}
