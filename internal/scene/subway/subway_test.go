package subway

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestNoLineIsDrawnRed(t *testing.T) {
	for _, hour := range []int{0, 7, 12, 19, 22} {
		for _, dark := range []bool{true, false} {
			pl := paletteAt(time.Date(2026, 1, 1, hour, 0, 0, 0, time.Local), dark)
			for i, c := range pl.lines {
				if h, sat := hue(c); sat > .3 && (h < 22 || h > 338) {
					t.Errorf("%02d:00 dark=%v: line color %d %s is red (hue %.0f)", hour, dark, i, c.Hex(), h)
				}
			}
		}
	}
}

func hue(c rgb) (float64, float64) {
	hi, lo := math.Max(c.R, math.Max(c.G, c.B)), math.Min(c.R, math.Min(c.G, c.B))
	if hi == lo {
		return 0, 0
	}
	d := hi - lo
	var h float64
	switch hi {
	case c.R:
		h = math.Mod((c.G-c.B)/d, 6)
	case c.G:
		h = (c.B-c.R)/d + 2
	default:
		h = (c.R-c.G)/d + 4
	}
	return math.Mod(h*60+360, 360), d / hi
}

// crowded is one repository of n units, the selected one and one that needs you last of all.
func crowded(n int) scene.Scene {
	r := scene.Range{ID: "repo:r", Name: "repo"}
	for i := range n {
		r.Peaks = append(r.Peaks, scene.Peak{ID: fmt.Sprint("u", i), Name: fmt.Sprint("unit ", i), Progress: .5,
			Team: []scene.Climber{{Stance: scene.Climbing}}, NeedsYou: i == n-2})
	}
	return scene.Scene{Dark: true, Selected: fmt.Sprint("u", n-1), Ranges: []scene.Range{r}}
}

func TestACrowdedMapKeepsTheSelectionAndAlertsOnScreen(t *testing.T) {
	for _, n := range []int{3, 12, 40} {
		for _, size := range []struct{ w, h, rows int }{{72, 24, 8}, {80, 24, 12}, {120, 40, 20}, {200, 60, 20}, {260, 70, 30}} {
			t.Run(fmt.Sprintf("%d units %dx%d", n, size.w, size.h), func(t *testing.T) {
				s := crowded(n)
				box := scene.TableBox(strings.Repeat("row\n", size.rows), size.w, size.h)
				found := map[string]bool{}
				for y := range size.h {
					for x := range size.w {
						if !box.Has(x, y) {
							found[Subway{}.Hit(s, size.w, size.h, box, x, y)] = true
						}
					}
				}
				for _, id := range []string{s.Selected, fmt.Sprint("u", n-2), "repo:r"} {
					if !found[id] {
						t.Errorf("%s is nowhere on the map", id)
					}
				}
			})
		}
	}
}
