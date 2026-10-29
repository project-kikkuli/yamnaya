package kaiju

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func lone(f func(*scene.Peak)) scene.Scene {
	p := scene.Peak{ID: "u", Name: "the unit", Progress: .5, Team: []scene.Climber{{Name: "a"}}, Camps: []scene.Camp{{Label: "#1", Stage: 2}}}
	f(&p)
	return scene.Scene{Ranges: []scene.Range{{ID: "repo:r", Name: "repo", Peaks: []scene.Peak{{ID: "left", Name: "left"}, p}}}}
}

func find(t *testing.T, s scene.Scene) monster {
	t.Helper()
	for _, m := range plan(s, 160, 48).monsters {
		if m.id == "u" {
			return m
		}
	}
	t.Fatal("no monster u")
	return monster{}
}

func TestAMonsterWadesInlandAsItsWorkGetsDone(t *testing.T) {
	var foot, water float64
	for i, progress := range []float64{0, .25, .5, .75, 1} {
		m := find(t, lone(func(p *scene.Peak) { p.Progress = progress }))
		if i > 0 && (m.foot >= foot || m.foot-m.water > foot-water) {
			t.Errorf("at %v%%: stands at %v with %v under water, was %v with %v", progress*100, m.foot, m.foot-m.water, foot, foot-water)
		}
		foot, water = m.foot, m.water
	}
}

func TestAMonsterKeepsItsPlaceWhateverHappensToIt(t *testing.T) {
	base := find(t, lone(func(*scene.Peak) {}))
	for name, f := range map[string]func(*scene.Peak){
		"waiting":   func(p *scene.Peak) { p.Team[0].Stance = scene.Waiting },
		"needs you": func(p *scene.Peak) { p.NeedsYou = true },
		"storm":     func(p *scene.Peak) { p.Weather = scene.Storm },
		"renamed":   func(p *scene.Peak) { p.Name = "another name" },
		"dying":     func(p *scene.Peak) { p.Dying = .7 },
	} {
		if m := find(t, lone(f)); m.x != base.x || m.foot != base.foot {
			t.Errorf("%s: moved from %v,%v to %v,%v", name, base.x, base.foot, m.x, m.foot)
		}
	}
}
