package themes

import (
	"fmt"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

var labelSizes = []struct{ w, h int }{{80, 24}, {120, 40}, {200, 60}}

// few is a small board: five goals in one repository whose names share their first words, as real goals do.
func few(theme string) scene.Scene {
	names := []string{"theme forest", "theme skyline", "fix login redirect", "fix login timeout", "billing"}
	g := scene.Range{ID: "repo:yamnaya", Name: "yamnaya"}
	for i, name := range names {
		p := scene.Peak{ID: fmt.Sprint("f", i), Name: name, Weather: scene.Weather(i % 5), Progress: float64(i) / 4, NeedsYou: i == 2,
			Team:  []scene.Climber{{Name: fmt.Sprint("agent-", i), Stance: scene.Stance(i % 3)}},
			Camps: []scene.Camp{{Label: fmt.Sprint("#", 40+i), State: scene.CampState(i % 4), Stage: i + 1}}}
		g.Peaks = append(g.Peaks, p)
	}
	return scene.Scene{Theme: theme, Dark: true, Selected: "f1", Ranges: []scene.Range{g}}
}

// name is a name the scene gives something, and the names of its kind that a cut of it must stay apart from.
type name struct {
	text  []rune
	kin   []string
	named bool
}

// names is every name and label of a scene: units, groups and agents, which may be cut, and pull requests.
func names(s scene.Scene) []name {
	var units, groups, agents, prs []string
	s.Walk(func(p scene.Peak) {
		units = append(units, p.Name)
		for _, c := range p.Team {
			agents = append(agents, c.Name)
		}
		for _, c := range p.Camps {
			prs = append(prs, c.Label)
		}
	}, func(r scene.Range) { groups = append(groups, r.Name) })
	var out []name
	for _, kind := range []struct {
		all   []string
		named bool
	}{{units, true}, {groups, true}, {agents, true}, {prs, false}} {
		for _, n := range kind.all {
			out = append(out, name{text: []rune(n), kin: kind.all, named: kind.named})
		}
	}
	return out
}

// signalRunes are the runes of the glyphs labels keep a blank cell from: agents, pull requests and alerts.
func signalRunes(th scene.Theme, s scene.Scene) string {
	var b strings.Builder
	for _, e := range th.Legend(s).Entries {
		switch e.Signal {
		case scene.SigBusy, scene.SigWaiting, scene.SigResting, scene.SigPR, scene.SigPRGreen, scene.SigPRRed,
			scene.SigMerged, scene.SigNeedsYou:
			for _, r := range e.Glyph {
				if g := scene.Glyph(r); !scene.IsText(g) && !strings.ContainsRune(" ▀▄█", r) {
					b.WriteRune(r)
				}
			}
		}
	}
	return b.String()
}

// labelFaults is every label on a canvas that touches another label or a signal glyph on its row, and every name
// cut to too few characters to tell it from the others of its kind.
func labelFaults(c *scene.Canvas, w, h int, all []name, signals string) []string {
	var out []string
	box := c.Box()
	for y := range h {
		row := make([]string, w)
		for x := range w {
			if _, g := c.Cell(x, y); g != "" && !box.Has(x, y) {
				row[x] = g
			} else {
				row[x] = " "
			}
		}
		at := func(x int) string {
			if x < 0 || x >= w {
				return " "
			}
			return row[x]
		}
		touches := func(x int) bool { g := at(x); return scene.IsText(g) || strings.Contains(signals, g) }
		line := func(x0, x1 int) string { return strings.Join(row[max(0, x0-3):min(w, x1+3)], "") }
		for x := 0; x < w; {
			best, k := -1, 0
			for i, n := range all {
				m := 0
				for m < len(n.text) && x+m < w && at(x+m) == string(n.text[m]) {
					m++
				}
				if m > k || m == k && m == len(n.text) {
					best, k = i, m
				}
			}
			for k > 0 && at(x+k-1) == " " {
				k--
			}
			if best < 0 || k < 2 {
				x++
				continue
			}
			n, end := all[best], x+k
			if k < len(n.text) {
				cut := at(end) == scene.Ellipsis
				if !n.named || !cut && at(end) != " " || touches(x-1) {
					x++
					continue
				}
				if cut {
					end++
				}
				shared := 0
				for _, other := range n.kin {
					if strings.HasPrefix(other, string(n.text[:k])) {
						shared++
					}
				}
				if k < scene.ShortMin || shared > 1 {
					out = append(out, fmt.Sprintf("row %d: %q cut to %q, %d of %d names start so", y, string(n.text), string(n.text[:k]), shared, len(n.kin)))
				}
			}
			if touches(x-1) || touches(end) {
				out = append(out, fmt.Sprintf("row %d: %q touches its neighbor in %q", y, string(n.text[:k]), line(x, end)))
			}
			x = end
		}
	}
	return out
}

func TestLabelsKeepApartAndNamesStayTellable(t *testing.T) {
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			fail := failer(t, th.Name())
			for _, board := range []struct {
				name string
				s    scene.Scene
			}{{"busy", busy(th.Name(), 0)}, {"five units", few(th.Name())}} {
				all, signals := names(board.s), signalRunes(th, board.s)
				for _, size := range labelSizes {
					for _, frame := range []int{0, 4} {
						box := scene.TableBox(table(8), size.w, size.h)
						if got := labelFaults(th.Paint(board.s, size.w, size.h, frame, box), size.w, size.h, all, signals); len(got) > 0 {
							fail("%s board %dx%d frame %d: %d label faults, e.g.\n\t%s", board.name, size.w, size.h, frame, len(got),
								strings.Join(got[:min(6, len(got))], "\n\t"))
						}
					}
				}
			}
		})
	}
}
