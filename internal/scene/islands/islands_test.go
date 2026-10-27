package islands

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestAnIslandKeepsItsPlaceWhateverHappensOnIt(t *testing.T) {
	base := func(f func(*scene.Peak)) scene.Scene {
		p := scene.Peak{ID: "u", Name: "the unit", Progress: .5, Team: []scene.Climber{{Name: "a"}}, Camps: []scene.Camp{{Label: "#1", Stage: 2}}}
		f(&p)
		return scene.Scene{Ranges: []scene.Range{{ID: "repo:r", Name: "repo", Peaks: []scene.Peak{{ID: "left", Name: "left"}, p}}}}
	}
	place := func(s scene.Scene) (float64, float64) {
		l := plan(s, 160, 3, 96*sea)
		for _, is := range l.isles {
			if is.id == "u" {
				return is.cx, is.cy
			}
		}
		t.Fatal("no island u")
		return 0, 0
	}
	x, y := place(base(func(*scene.Peak) {}))
	for name, f := range map[string]func(*scene.Peak){
		"waiting":   func(p *scene.Peak) { p.Team[0].Stance = scene.Waiting },
		"needs you": func(p *scene.Peak) { p.NeedsYou = true },
		"storm":     func(p *scene.Peak) { p.Weather = scene.Storm },
		"progress":  func(p *scene.Peak) { p.Progress = 1 },
		"renamed":   func(p *scene.Peak) { p.Name = "another name" },
		"dying":     func(p *scene.Peak) { p.Dying = .7 },
	} {
		if gx, gy := place(base(f)); gx != x || gy != y {
			t.Errorf("%s: island moved from %v,%v to %v,%v", name, x, y, gx, gy)
		}
	}
}
