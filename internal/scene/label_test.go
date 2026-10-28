package scene

import (
	"strings"
	"testing"
)

func TestShortKeepsNamesTellableOrRefuses(t *testing.T) {
	goals := []string{"theme forest", "theme skyline", "fix login redirect", "fix login timeout", "billing"}
	for _, c := range []struct {
		name string
		room int
		want string
		ok   bool
	}{
		{"billing", 7, "billing", true},
		{"billing", 6, "", false},
		{"theme skyline", 20, "theme skyline", true},
		{"theme skyline", 9, "theme sk…", true},
		{"theme skyline", 8, "", false},
		{"fix login redirect", 12, "fix login r…", true},
		{"fix login redirect", 11, "", false},
	} {
		got, ok := Short(c.name, goals, c.room)
		if got != c.want || ok != c.ok {
			t.Errorf("Short(%q, %d) = %q, %v; want %q, %v", c.name, c.room, got, ok, c.want, c.ok)
		}
	}
}

func TestUniquePrefixNeedsAllOfANameThatStartsAnother(t *testing.T) {
	names := []string{"ship unit 1", "ship unit 10", "ship unit 2", "solo"}
	for name, want := range map[string]int{"ship unit 1": 11, "ship unit 10": 12, "ship unit 2": 11, "solo": 2} {
		if got := UniquePrefix(name, names); got != want {
			t.Errorf("UniquePrefix(%q) = %d, want %d", name, got, want)
		}
	}
}

func TestLabelsKeepABlankCellBetweenLabelsAndReservedGlyphs(t *testing.T) {
	Colors = TrueColor
	c := NewCanvas(20, 3, Rect{X0: 15, Y0: 0, X1: 20, Y1: 3})
	l := NewLabels(c)
	l.Reserve(4, 0, 1)
	for _, step := range []struct {
		text string
		at   [][2]int
		x, y int
		ok   bool
	}{
		{"ab", [][2]int{{5, 0}, {6, 0}}, 6, 0, true},
		{"cd", [][2]int{{8, 0}, {9, 0}}, 9, 0, true},
		{"ef", [][2]int{{12, 0}, {14, 0}, {0, 1}}, 12, 0, true},
		{"gh", [][2]int{{14, 1}}, 0, 0, false},
		{"ij", [][2]int{{19, 2}}, 0, 0, false},
	} {
		x, y, ok := l.Place(step.text, White, step.at...)
		if x != step.x || y != step.y || ok != step.ok {
			t.Errorf("Place(%q) at %d,%d %v; want %d,%d %v", step.text, x, y, ok, step.x, step.y, step.ok)
		}
	}
	var row strings.Builder
	for x := range 15 {
		_, g := c.Cell(x, 0)
		row.WriteString(g)
	}
	if got := row.String(); got != "      ab cd ef " {
		t.Errorf("row 0 = %q", got)
	}
}
