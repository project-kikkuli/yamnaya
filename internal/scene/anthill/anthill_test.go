package anthill

import (
	"strconv"
	"strings"
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

// busy is a crowded board: 30 units in six colonies and a nested chief, agents in every stance and pull
// requests in every state, every weather and alarm.
func busy() scene.Scene {
	s := scene.Scene{Dark: true, Selected: "u7"}
	unit := func(n int) scene.Peak {
		p := scene.Peak{ID: strconv.Itoa(n), Name: "ship unit " + strconv.Itoa(n), Weather: scene.Weather(n % 5), Progress: float64(n%11) / 10, NeedsYou: n%6 == 0}
		for a := range 3 {
			p.Team = append(p.Team, scene.Climber{Name: "agent-" + strconv.Itoa(n) + "-" + strconv.Itoa(a), Stance: scene.Stance((n + a) % 3)})
		}
		for c := range 3 {
			p.Camps = append(p.Camps, scene.Camp{Label: "#" + strconv.Itoa(100+n*3+c), State: scene.CampState((n + c) % 4), Stage: (n + c) % 7})
		}
		return p
	}
	n := 0
	for range 5 {
		g := scene.Range{ID: "repo" + strconv.Itoa(n), Name: "repo"}
		for range 5 {
			g.Peaks = append(g.Peaks, unit(n))
			n++
		}
		s.Ranges = append(s.Ranges, g)
	}
	chief := scene.Range{ID: "chief", Name: "chief", Ranges: []scene.Range{{ID: "sub", Name: "nested"}}}
	for range 3 {
		chief.Ranges[0].Peaks = append(chief.Ranges[0].Peaks, unit(n))
		n++
	}
	s.Ranges = append(s.Ranges, chief)
	return s
}

func TestLabelsNeverTouchAnotherGlyph(t *testing.T) {
	marks := "жЖ○●▒◆ψ╱▾"
	for _, size := range []struct{ w, h int }{{80, 24}, {120, 40}, {200, 60}, {260, 70}} {
		for frame := range 8 {
			p := still(busy(), size.w, size.h).at(frame, scene.Rect{})
			p.motion()
			for y := range p.p / 2 {
				for x := 1; x < p.w; x++ {
					a, b := p.cells[y*p.w+x-1], p.cells[y*p.w+x]
					if !a.set || !b.set || a.cont || b.cont || a.s == " " || b.s == " " {
						continue
					}
					if strings.Contains(marks, a.s) != strings.Contains(marks, b.s) || a.s == "%" {
						t.Errorf("%dx%d frame %d: %q touches %q at %d,%d", size.w, size.h, frame, a.s, b.s, x, y)
					}
				}
			}
			p.release()
		}
	}
}
