package scene

import (
	"hash/fnv"
	"math"
)

// SeedOf is a stable seed for a name, so the same unit draws the same way every frame.
func SeedOf(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// Spread is splitmix64: a well-mixed hash of a seed and a lattice index.
func Spread(seed uint64, i int) uint64 {
	z := seed + uint64(i)*0x9e3779b97f4a7c15
	z = (z ^ z>>30) * 0xbf58476d1ce4e5b9
	z = (z ^ z>>27) * 0x94d049bb133111eb
	return z ^ z>>31
}

// Unit is a hash of a seed and an index in 0..1.
func Unit(seed uint64, i int) float64 { return float64(Spread(seed, i)>>11) / (1 << 53) }

// Noise is smooth value noise along x in 0..1.
func Noise(seed uint64, x float64) float64 {
	i := math.Floor(x)
	f := x - i
	f = f * f * (3 - 2*f)
	return Unit(seed, int(i))*(1-f) + Unit(seed, int(i)+1)*f
}

func Fbm(seed uint64, x float64, octaves int) float64 {
	sum, amp, norm := 0.0, 1.0, 0.0
	for o := range octaves {
		sum += Noise(seed+uint64(o)*7919, x) * amp
		norm += amp
		x, amp = x*2.03, amp*.5
	}
	return sum / norm
}

func Noise2(seed uint64, x, y float64) float64 {
	j := math.Floor(y)
	f := y - j
	f = f * f * (3 - 2*f)
	return Noise(Spread(seed, int(j)), x)*(1-f) + Noise(Spread(seed, int(j)+1), x)*f
}

func Fbm2(seed uint64, x, y float64, octaves int) float64 {
	sum, amp, norm := 0.0, 1.0, 0.0
	for o := range octaves {
		sum += Noise2(seed+uint64(o)*104729, x, y) * amp
		norm += amp
		x, y, amp = x*2.1, y*2.1, amp*.5
	}
	return sum / norm
}
