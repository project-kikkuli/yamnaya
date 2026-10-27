package scene

import (
	"fmt"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// Frame draws the scene's theme filling width x height with the table floating clean on top, low and centered.
// The same scene, size and frame always give the same picture.
func Frame(s Scene, width, height, frame int, table string) string {
	c := Paint(s, width, height, frame, table)
	if c == nil {
		return ""
	}
	painted.Lock()
	if painted.drawn && painted.c == c && painted.table == table {
		out := painted.out
		painted.Unlock()
		return out
	}
	painted.Unlock()
	out := c.Draw(table)
	painted.Lock()
	painted.c, painted.table, painted.out, painted.drawn = c, table, out, true
	painted.Unlock()
	return out
}

// Paint is the theme's picture of a frame around where the table floats, cell by cell.
func Paint(s Scene, width, height, frame int, table string) *Canvas {
	if width <= 0 || height <= 0 {
		return nil
	}
	if ReducedMotion {
		frame = 0
	}
	t := Lookup(s.Theme)
	_, _, box := place(table, width, height)
	key := fmt.Sprintf("%s %d %d %d %v %v %v %q %v %v", t.Name(), width, height, frame, s.Dark, s.Now, s.Ranges, s.Selected, s.Peaks, box)
	painted.Lock()
	if painted.key == key {
		c := painted.c
		painted.Unlock()
		return c
	}
	painted.Unlock()
	c := t.Paint(s, width, height, frame, box)
	painted.Lock()
	painted.key, painted.c, painted.drawn = key, c, false
	painted.Unlock()
	return c
}

// painted is the last frame painted and drawn, returned as is when nothing that shapes it changed.
var painted struct {
	sync.Mutex
	key, table, out string
	c               *Canvas
	drawn           bool
}

// Hit is the id of the unit or group drawn at cell x, y of the frame Frame draws for the same scene, size and
// table; "" over the background or the table.
func Hit(s Scene, width, height int, table string, x, y int) string {
	if x < 0 || y < 0 || x >= width || y >= height {
		return ""
	}
	_, _, box := place(table, width, height)
	if box.Has(x, y) {
		return ""
	}
	return Lookup(s.Theme).Hit(s, width, height, box, x, y)
}

// TableBox is where Frame floats a table on a screen of width x height.
func TableBox(table string, width, height int) Rect {
	_, _, box := place(table, width, height)
	return box
}

// place clips the table to the screen and sets it centered with its bottom edge a few rows above the screen's,
// growing upward, with a column of margin when there is room. Nothing about the scene depends on its height.
func place(table string, width, height int) ([]string, int, Rect) {
	var lines []string
	if table != "" {
		lines = strings.Split(strings.TrimRight(table, "\n"), "\n")
	}
	lines = lines[:min(len(lines), height)]
	tw := 0
	for i, line := range lines {
		lines[i] = ansi.Truncate(line, width, "")
		tw = max(tw, ansi.StringWidth(lines[i]))
	}
	var box Rect
	if len(lines) > 0 {
		pad := min(1, (width-tw)/2)
		x := (width - tw) / 2
		bottom := height - min(3, height/24)
		y := max(0, bottom-len(lines))
		box = Rect{x - pad, y, x + tw + pad, y + len(lines)}
	}
	return lines, tw, box
}
