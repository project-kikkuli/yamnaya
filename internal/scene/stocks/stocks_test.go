package stocks

import (
	"slices"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestSymbolIsMadeFromTheName(t *testing.T) {
	for name, want := range map[string]string{
		"Fix the login flow on mobile": "FLFM",
		"ship unit 12":                 "SU12",
		"the unit":                     "UNIT",
		"right":                        "RGHT",
		"Migrate auth to AuthZed":      "MAA",
		"mountains":                    "MNTN",
		"release 2026":                 "RLS26",
		"":                             "N/A",
	} {
		if got := symbol(name); got != want {
			t.Errorf("symbol(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestWalkEndsOnProgressAndTradesAsItsWeather(t *testing.T) {
	for _, w := range []scene.Weather{scene.Clear, scene.Clouds, scene.Storm, scene.Eruption, scene.Summit} {
		for _, p := range []float64{0, .3, .75, 1} {
			for _, n := range []int{1, 7, 60} {
				vals := make([]float64, n)
				walk(scene.SeedOf("goal"), vals, p, w)
				if vals[n-1] != p {
					t.Errorf("weather %d progress %v n %d: ends at %v", w, p, n, vals[n-1])
				}
				if slices.Min(vals) < 0 || slices.Max(vals) > 1 {
					t.Errorf("weather %d progress %v n %d: leaves 0..1", w, p, n)
				}
				switch {
				case w == scene.Summit && slices.Max(vals) > p:
					t.Errorf("progress %v n %d: an all-time high tops itself", p, n)
				case w == scene.Storm && n > 7 && p < 1 && slices.Max(vals) <= p:
					t.Errorf("progress %v n %d: a crash never fell", p, n)
				case w == scene.Clouds && n > 7 && vals[n-2] != p:
					t.Errorf("progress %v n %d: flat trading still moves", p, n)
				}
			}
		}
	}
}
