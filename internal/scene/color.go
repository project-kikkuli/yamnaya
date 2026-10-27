package scene

import (
	"math"
	"strconv"
)

// RGB is a color with channels 0..1.
type RGB struct{ R, G, B float64 }

// White is full white.
var White = RGB{1, 1, 1}

// Hex reads a color written #rrggbb.
func Hex(s string) RGB {
	v, _ := strconv.ParseUint(s[1:], 16, 32)
	return RGB{float64(v>>16&255) / 255, float64(v>>8&255) / 255, float64(v&255) / 255}
}

// Mix is a moved toward b by t, 0..1.
func Mix(a, b RGB, t float64) RGB {
	t = Clamp(t, 0, 1)
	return RGB{a.R + (b.R-a.R)*t, a.G + (b.G-a.G)*t, a.B + (b.B-a.B)*t}
}

func (c RGB) Scale(k float64) RGB { return RGB{c.R * k, c.G * k, c.B * k} }

func (c RGB) Luma() float64 { return .2126*c.R + .7152*c.G + .0722*c.B }

// Bytes is the color as 0..255 channels.
func (c RGB) Bytes() (int, int, int) {
	f := func(v float64) int { return int(math.Round(Clamp(v, 0, 1) * 255)) }
	return f(c.R), f(c.G), f(c.B)
}

// Hex is the color written #rrggbb.
func (c RGB) Hex() string {
	r, g, b := c.Bytes()
	const digits = "0123456789abcdef"
	return string([]byte{'#', digits[r>>4], digits[r&15], digits[g>>4], digits[g&15], digits[b>>4], digits[b&15]})
}

func Clamp(v, lo, hi float64) float64 {
	if !(v >= lo) {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// Smooth is the smoothstep from lo to hi; lo above hi runs it backward.
func Smooth(lo, hi, v float64) float64 {
	t := Clamp((v-lo)/(hi-lo), 0, 1)
	return t * t * (3 - 2*t)
}

func Lerp(a, b, t float64) float64 { return a + (b-a)*Clamp(t, 0, 1) }

// Readable keeps a color's hue but moves it away from the background until the two contrast.
func Readable(fg, bg RGB) RGB {
	if math.Abs(fg.Luma()-bg.Luma()) >= .4 {
		return fg
	}
	if bg.Luma() > .5 {
		return fg.Scale(.3)
	}
	return Mix(fg, White, .6)
}
