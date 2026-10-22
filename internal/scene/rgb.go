package scene

import (
	"math"
	"strconv"
)

type rgb struct{ r, g, b float64 }

func hex(s string) rgb {
	v, _ := strconv.ParseUint(s[1:], 16, 32)
	return rgb{float64(v>>16&255) / 255, float64(v>>8&255) / 255, float64(v&255) / 255}
}

func mix(a, b rgb, t float64) rgb {
	t = clamp(t, 0, 1)
	return rgb{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

func (c rgb) scale(k float64) rgb { return rgb{c.r * k, c.g * k, c.b * k} }

func (c rgb) luma() float64 { return .2126*c.r + .7152*c.g + .0722*c.b }

func (c rgb) bytes() (int, int, int) {
	f := func(v float64) int { return int(math.Round(clamp(v, 0, 1) * 255)) }
	return f(c.r), f(c.g), f(c.b)
}

func clamp(v, lo, hi float64) float64 {
	if !(v >= lo) {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func smooth(lo, hi, v float64) float64 {
	t := clamp((v-lo)/(hi-lo), 0, 1)
	return t * t * (3 - 2*t)
}

func lerp(a, b, t float64) float64 { return a + (b-a)*clamp(t, 0, 1) }
