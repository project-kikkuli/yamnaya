package civilizations

import (
	"fmt"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// worldOf is n repositories of five goals each under a chief with a nested chief, every sixth goal needing the
// owner, with sel selected.
func worldOf(n int, sel string) scene.Scene {
	s := scene.Scene{Theme: "civilizations", Dark: true, Selected: sel}
	goal := func(u int) scene.Peak {
		return scene.Peak{ID: fmt.Sprint("u", u), Name: fmt.Sprint("goal ", u), NeedsYou: u%6 == 0, Progress: .5,
			Team: []scene.Climber{{Stance: scene.Climbing}}, Camps: []scene.Camp{{State: scene.Green, Stage: 2}}}
	}
	for r := range n {
		g := scene.Range{ID: fmt.Sprint("repo:", r), Name: fmt.Sprint("repo-", r)}
		for k := range 5 {
			g.Peaks = append(g.Peaks, goal(r*5+k))
		}
		s.Ranges = append(s.Ranges, g)
	}
	s.Ranges = append(s.Ranges, scene.Range{ID: "chief", Name: "chief", Peaks: []scene.Peak{goal(n * 5)},
		Ranges: []scene.Range{{ID: "sub", Name: "sub chief", Peaks: []scene.Peak{goal(n*5 + 1)}}}})
	return s
}

func TestACrowdedMapKeepsTheSelectionAndEveryAlertInSight(t *testing.T) {
	for _, size := range []struct{ w, h, repos int }{{80, 24, 2}, {80, 24, 8}, {120, 40, 8}, {200, 60, 12}, {260, 70, 3}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := worldOf(size.repos, fmt.Sprint("u", size.repos*5-2))
			c := Civilizations{}.Paint(s, size.w, size.h, 0, scene.Rect{})
			marks, flags := 0, 0
			for y := range size.h {
				for x := range size.w {
					switch _, g := c.Cell(x, y); g {
					case "▾":
						marks++
					case flag:
						flags++
					}
				}
			}
			if marks != 1 {
				t.Errorf("%d selection markers, want 1", marks)
			}
			want := 0
			l := plan(s, size.w, size.h)
			for _, n := range l.lands {
				for _, cv := range n.civs {
					if cv.needs {
						want++
					}
				}
			}
			if want == 0 || flags != want {
				t.Errorf("%d envoy flags for %d civilizations that need you", flags, want)
			}
			if bottom := l.row(l.bands-1) + bandRows; bottom > limit(size.h) {
				t.Errorf("continents run to row %d, past the %d above the table", bottom, limit(size.h))
			}
		})
	}
}

func TestAClickOnTheMapSelectsWhatIsDrawnThere(t *testing.T) {
	s := worldOf(2, "")
	l := plan(s, 200, 60)
	at := func(id string) (int, int) {
		for _, n := range l.lands {
			for _, cv := range n.civs {
				if cv.id == id {
					return cv.x + 2, l.row(n.band) + 3
				}
			}
		}
		for _, e := range l.empires {
			if e.id == id {
				return e.x0, l.row(e.band) + 3
			}
		}
		t.Fatalf("%s is not on the map", id)
		return 0, 0
	}
	for _, id := range []string{"u0", "u7", "u10", "u11", "chief"} {
		x, y := at(id)
		if got := (Civilizations{}).Hit(s, 200, 60, scene.Rect{}, x, y); got != id {
			t.Errorf("click at %d,%d hit %q, want %q", x, y, got, id)
		}
	}
	if got := (Civilizations{}).Hit(s, 200, 60, scene.Rect{}, 0, 59); got != "" {
		t.Errorf("open sea hit %q", got)
	}
}
