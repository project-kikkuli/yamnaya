package scene

import (
	"math"
	"strconv"
)

// RGB is a color with channels 0..1.
type RGB struct{ R, G, B float64 }

// White and Black are full white and black.
var (
	White = RGB{1, 1, 1}
	Black = RGB{}
)

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

// Readable is fg and bg moved apart until fg reads on bg at MinContrast with a margin, fg keeping its hue and never
// lightening, so a red glyph stays red: a glyph darker than its cell darkens, then the cell lightens; a lighter
// glyph darkens its cell, toward black, and only a glyph too dark for that lightens the cell instead.
func Readable(fg, bg RGB) (RGB, RGB) {
	const want = MinContrast * 1.1
	if Contrast(fg, bg) >= want {
		return fg, bg
	}
	lighten := func(fg, bg RGB) (RGB, RGB) {
		for k := .2; k <= 1; k += .1 {
			if b := Mix(bg, White, k); Contrast(fg, b) >= want {
				return fg, b
			}
		}
		return fg, White
	}
	if bg.lum() > fg.lum() {
		for k := .9; k >= .3; k -= .1 {
			if f := fg.Scale(k); Contrast(f, bg) >= want {
				return f, bg
			}
		}
		return lighten(fg.Scale(.3), bg)
	}
	for k := .75; k > .01; k *= .75 {
		if b := bg.Scale(k); Contrast(fg, b) >= want {
			return fg, b
		}
	}
	if Contrast(fg, Black) >= want {
		return fg, Black
	}
	return lighten(fg, bg)
}

// Backing is bg darkened, or for a glyph too dark for that lightened, just enough that fg reads on it at
// MinContrast with a margin, fg untouched: for glyphs whose color is their meaning, so busy and waiting agents
// keep the colors that tell them apart and red stays the red it was.
func Backing(fg, bg RGB) RGB {
	const want = MinContrast * 1.1
	if Contrast(fg, bg) >= want {
		return bg
	}
	darker := func() (RGB, bool) {
		for k := .75; k > .01; k *= .75 {
			if b := bg.Scale(k); Contrast(fg, b) >= want {
				return b, true
			}
		}
		return Black, Contrast(fg, Black) >= want
	}
	lighter := func() (RGB, bool) {
		for k := .2; k < 1; k += .1 {
			if b := Mix(bg, White, k); Contrast(fg, b) >= want {
				return b, true
			}
		}
		return White, Contrast(fg, White) >= want
	}
	first, second := darker, lighter
	if fg.lum() < bg.lum() {
		first, second = lighter, darker
	}
	if b, ok := first(); ok {
		return b
	}
	b, _ := second()
	return b
}

// IsRed reports whether a color reads as red, the color reserved for waiting agents, needs-you and failing CI:
// a hue within 15 degrees of pure red, saturation at least .5 and value at least .3 (HSV). Orange, from 16
// degrees, and pink or salmon washed below saturation .5 are not red.
func IsRed(c RGB) bool { return redness(c) <= 15 }

// NearRed reports whether a color is red or so close to it, a hue within 20 degrees of pure red, that it reads as
// red on some terminals. Nothing but a waiting agent, needs-you or failing CI may be drawn in it, glyph or pixel.
func NearRed(c RGB) bool { return redness(c) < 20 }

// redness is how many degrees the color's hue is from pure red, or 180 when it is too dark or washed out to read
// as red at all.
func redness(c RGB) float64 {
	hi, lo := max(c.R, c.G, c.B), min(c.R, c.G, c.B)
	if hi < .3 || hi-lo < .5*hi {
		return 180
	}
	var hue float64
	switch hi {
	case c.R:
		hue = math.Mod((c.G-c.B)/(hi-lo)*60+360, 360)
	case c.G:
		hue = (c.B-c.R)/(hi-lo)*60 + 120
	default:
		hue = (c.R-c.G)/(hi-lo)*60 + 240
	}
	return min(hue, 360-hue)
}

// MinContrast is the least contrast ratio a glyph that means something keeps with the background behind it.
const MinContrast = 3.0

// Contrast is the WCAG contrast ratio of two colors as a truecolor terminal draws them, from 1 for the same to 21
// for black on white.
func Contrast(a, b RGB) float64 {
	la, lb := a.drawnLum(), b.drawnLum()
	return (max(la, lb) + .05) / (min(la, lb) + .05)
}

// drawnLum is the luminance of the color as a truecolor terminal draws it, from a table of the 256 byte values.
func (c RGB) drawnLum() float64 {
	r, g, b := c.Bytes()
	return .2126*linear[r] + .7152*linear[g] + .0722*linear[b]
}

// linear is each byte value of a channel, linearized.
var linear = func() (out [256]float64) {
	for i := range out {
		out[i] = channel(float64(i) / 255)
	}
	return out
}()

// lum is the WCAG relative luminance.
func (c RGB) lum() float64 {
	return .2126*channel(c.R) + .7152*channel(c.G) + .0722*channel(c.B)
}

func channel(v float64) float64 {
	v = Clamp(v, 0, 1)
	if v <= .04045 {
		return v / 12.92
	}
	return math.Pow((v+.055)/1.055, 2.4)
}
