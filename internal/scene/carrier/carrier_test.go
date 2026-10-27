package carrier

import (
	"fmt"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// fleetOf is n repositories of five goals each, every sixth needing the owner, with sel selected.
func fleetOf(n int, sel string) scene.Scene {
	s := scene.Scene{Theme: "carrier", Dark: true, Selected: sel}
	for r := range n {
		g := scene.Range{ID: fmt.Sprint("repo:", r), Name: fmt.Sprint("repo-", r)}
		for k := range 5 {
			u := r*5 + k
			g.Peaks = append(g.Peaks, scene.Peak{ID: fmt.Sprint("u", u), Name: fmt.Sprint("goal ", u), NeedsYou: u%6 == 0,
				Team: []scene.Climber{{Stance: scene.Climbing}}, Camps: []scene.Camp{{State: scene.Green, Stage: 2}}})
		}
		s.Ranges = append(s.Ranges, g)
	}
	return s
}

func TestACrowdedScreenKeepsTheSelectionAndEveryAlertInSight(t *testing.T) {
	for _, size := range []struct{ w, h, repos int }{{80, 24, 2}, {80, 24, 8}, {120, 40, 8}, {200, 60, 12}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			s := fleetOf(size.repos, fmt.Sprint("u", size.repos*5-2))
			c := Carrier{}.Paint(s, size.w, size.h, 0, scene.Rect{})
			marks, flags := 0, 0
			for y := range size.h {
				for x := range size.w {
					switch _, g := c.Cell(x, y); g {
					case "▾":
						marks++
					case "▚":
						flags++
					}
				}
			}
			if marks != 1 {
				t.Errorf("%d selection markers, want 1", marks)
			}
			want := 0
			l := plan(s, size.w, size.h)
			for _, sh := range l.ships {
				for _, q := range sh.squads {
					if q.needs {
						want++
					}
				}
			}
			if want == 0 || flags != want {
				t.Errorf("%d signal flags for %d squadrons that need you", flags, want)
			}
			if l.row(l.bands-1)+bandRows > size.h {
				t.Errorf("bands run off the screen: %d bands from row %d", l.bands, l.top)
			}
		})
	}
}
