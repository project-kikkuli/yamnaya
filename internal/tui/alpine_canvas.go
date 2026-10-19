package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

const (
	alpineSky = iota
	alpineFar
	alpineRock
	alpineFace
	alpineIce
	alpineGlow
	alpineSnow
	alpineWater
	alpineLake
	alpineGold
	alpineFlare
	alpineLamp
	alpineCloud
	alpineStorm
)

type alpineCanvas struct {
	w, h int
	text []rune
	ink  []int
}

func newAlpineCanvas(w, h int) alpineCanvas {
	c := alpineCanvas{w: w, h: h, text: make([]rune, w*h), ink: make([]int, w*h)}
	for i := range c.text {
		c.text[i] = ' '
	}
	return c
}

func (c *alpineCanvas) put(x, y int, r rune, ink int) {
	if x >= 0 && x < c.w && y >= 0 && y < c.h {
		c.text[y*c.w+x], c.ink[y*c.w+x] = r, ink
	}
}

func (c *alpineCanvas) label(x, y int, text string, ink int) {
	for _, r := range text {
		width := ansi.StringWidth(string(r))
		if width == 0 || x+width > c.w {
			continue
		}
		c.put(x, y, r, ink)
		if width == 2 {
			c.put(x+1, y, 0, ink)
		}
		x += width
	}
}

func (c alpineCanvas) String() string {
	var b strings.Builder
	b.Grow(c.w * c.h * 4)
	for y := range c.h {
		for x := 0; x < c.w; {
			start, ink := x, c.ink[y*c.w+x]
			for x < c.w && c.ink[y*c.w+x] == ink {
				x++
			}
			text := strings.ReplaceAll(string(c.text[y*c.w+start:y*c.w+x]), "\x00", "")
			b.WriteString(pal.alpine[ink].Render(text))
		}
		b.WriteByte('\n')
	}
	return b.String()
}
