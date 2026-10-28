package scene

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ShortMin is the fewest characters a cut name keeps: fewer cannot tell goals apart at a glance.
const ShortMin = 8

// Ellipsis ends a cut name.
const Ellipsis = "…"

// UniquePrefix is how many leading characters of name no other of names shares: the least a cut must keep for the
// name to stay its own. A name that starts another, or that repeats, needs all of it.
func UniquePrefix(name string, names []string) int {
	n := []rune(name)
	need := 1
	seen := false
	for _, other := range names {
		if other == name && !seen {
			seen = true
			continue
		}
		o := []rune(other)
		k := 0
		for k < len(n) && k < len(o) && n[k] == o[k] {
			k++
		}
		need = max(need, k+1)
	}
	return min(need, len(n))
}

// Short is name fitted to room cells, cut with Ellipsis when it must be, keeping at least ShortMin characters and
// its UniquePrefix among names. ok is false when room is too small for that: fold such units ("5 trees") instead
// of cutting them further.
func Short(name string, names []string, room int) (string, bool) {
	n := []rune(name)
	if len(n) <= room {
		return name, true
	}
	keep := room - 1
	if keep < min(len(n), max(ShortMin, UniquePrefix(name, names))) {
		return "", false
	}
	return string(n[:keep]) + Ellipsis, true
}

// IsText reports whether a glyph reads as part of a word: a letter, a digit, or the marks labels are written
// with. Two labels, or a label and a signal, never touch on a row: a cell that is not text lies between them.
func IsText(glyph string) bool {
	r, _ := utf8.DecodeRuneInString(glyph)
	return unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("%#…", r)
}

// Labels places text on a canvas so that no two labels, and no label and a reserved cell, touch on a row: at least
// one blank cell lies between them. Reserve the cells of every signal glyph first, then Place each label.
type Labels struct {
	c    *Canvas
	used []bool
}

// NewLabels starts placing labels on c.
func NewLabels(c *Canvas) *Labels { return &Labels{c: c, used: make([]bool, c.w*c.h)} }

// Reserve marks w cells from x on row y as a glyph that labels keep a blank cell from.
func (l *Labels) Reserve(x, y, w int) {
	for i := max(0, x); i < min(l.c.w, x+w); i++ {
		if y >= 0 && y < l.c.h {
			l.used[y*l.c.w+i] = true
		}
	}
}

// Fits reports whether a label w cells wide fits at x, y: on the canvas, off the table, with a free cell on each
// side, none of it on or beside a placed label, a reserved cell or other text.
func (l *Labels) Fits(x, y, w int) bool {
	if w <= 0 || y < 0 || y >= l.c.h || x < 0 || x+w > l.c.w {
		return false
	}
	for i := x - 1; i <= x+w; i++ {
		if i < 0 || i >= l.c.w {
			continue
		}
		if l.used[y*l.c.w+i] || IsText(l.c.glyph[y*l.c.w+i]) {
			return false
		}
		if i >= x && i < x+w && l.c.box.Has(i, y) {
			return false
		}
	}
	return true
}

// Place draws text in fg at the first spot in at where it Fits, each cell made Readable on what was behind it,
// and reports which spot; ok is false, and nothing is drawn, when none fits.
func (l *Labels) Place(text string, fg RGB, at ...[2]int) (x, y int, ok bool) {
	w := utf8.RuneCountInString(text)
	for _, p := range at {
		if !l.Fits(p[0], p[1], w) {
			continue
		}
		x, y = p[0], p[1]
		i := 0
		for _, r := range text {
			l.draw(x+i, y, fg, Glyph(r))
			i++
		}
		l.Reserve(x, y, w)
		return x, y, true
	}
	return 0, 0, false
}

func (l *Labels) draw(x, y int, fg RGB, glyph string) {
	k := l.c.ink[y*l.c.w+x]
	if Colors == NoColor || k&inkColor == 0 {
		l.c.Plain(x, y, glyph)
		return
	}
	top, bottom, _ := l.c.Ink(x, y)
	behind := bottom
	switch l.c.glyph[y*l.c.w+x] {
	case "█":
		behind = top
	case "▀", "▄":
		behind = Mix(top, bottom, .5)
	}
	f, b := Readable(fg, behind)
	l.c.Set(x, y, f, b, glyph)
}
