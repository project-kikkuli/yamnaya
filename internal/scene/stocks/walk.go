package stocks

import (
	"math"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

const (
	// knee is where a crash starts falling, flat trading levels off and trading halts, as a share of the chart.
	crashKnee, flatKnee, haltKnee = .8, .65, .78
)

// walk fills out with a unit's price over time: a seeded wander from near zero that ends on its progress. A
// crash spikes and falls to it, flat trading reaches it early and holds, and an all-time high never tops it.
func walk(seed uint64, out []float64, progress float64, w scene.Weather) {
	n := len(out)
	if n == 0 {
		return
	}
	p := scene.Clamp(progress, 0, 1)
	start := math.Min(p, .03+.1*scene.Unit(seed, 1))
	knee, top := 1.0, p
	switch w {
	case scene.Storm:
		knee, top = crashKnee, math.Min(1, p+.22+.12*scene.Unit(seed, 2))
	case scene.Clouds:
		knee = flatKnee
	}
	f0, f1 := wander(seed, 0), wander(seed, 1)
	for i := range out {
		t := 1.0
		if n > 1 {
			t = float64(i) / float64(n-1)
		}
		var v float64
		if t <= knee {
			u := t / knee
			v = start + (top-start)*u + .35*(wander(seed, u)-(1-u)*f0-u*f1)
		} else if w == scene.Storm {
			v = top + (p-top)*math.Pow((t-knee)/(1-knee), .6)
		} else {
			v = p
		}
		v = scene.Clamp(v, 0, 1)
		if w == scene.Summit {
			v = math.Min(v, p)
		}
		out[i] = v
	}
	out[n-1] = p
}

func wander(seed uint64, u float64) float64 { return scene.Fbm(seed, u*9+.5, 4) - .5 }
