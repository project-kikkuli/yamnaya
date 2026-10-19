package tui

import "math"

type massif struct {
	key, name, region string
	frame             uint64
	relief            float64
	needs             int
	active            bool
}

func (m massif) height(frame uint64) float64 {
	return m.relief * math.Pow(0.998, float64(frame-m.frame))
}
