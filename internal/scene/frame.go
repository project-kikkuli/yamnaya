package scene

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// Frame draws the mountains filling width x height with the table floating clean on top, low and centered.
// The same scene, size and frame always give the same picture.
func Frame(s Scene, width, height, frame int, table string) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if reducedMotion {
		frame = 0
	}
	lines, tw, box := place(table, width, height)
	p := newPainter(s, width, height, frame, box)
	p.paint()
	return p.encode(lines, tw)
}

// Hit is the id of the peak or range drawn at cell x, y of the frame Frame draws for the same scene, size and table,
// its name and team included; "" over sky, water or the table.
func Hit(s Scene, width, height int, table string, x, y int) string {
	if x < 0 || y < 0 || x >= width || y >= height {
		return ""
	}
	_, _, box := place(table, width, height)
	if box.has(x, y) {
		return ""
	}
	p := newPainter(s, width, height, 0, box)
	p.paint()
	if i := p.who[y*p.w+x] - 1; i >= 0 {
		return p.l.mounts[i].id
	}
	for d := len(p.layers) - 1; d >= 0; d-- {
		lay := p.layers[d]
		if float64(2*y+1) < float64(p.hz)-lay.hm[x] || 2*y >= p.hz {
			continue
		}
		if i := lay.own[x]; i >= 0 {
			return p.l.mounts[i].id
		}
		for _, m := range p.l.massifs {
			if m.depth == d && float64(x) >= m.x0 && float64(x) < m.x1 {
				return m.id
			}
		}
	}
	return ""
}

// place clips the table to the screen and sets it low and centered, with a column of margin when there is room.
func place(table string, width, height int) ([]string, int, rect) {
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
	var box rect
	if len(lines) > 0 {
		pad := min(1, (width-tw)/2)
		x := (width - tw) / 2
		free := height - len(lines)
		y := height - len(lines) - min(free/4, 3)
		box = rect{x - pad, y, x + tw + pad, y + len(lines)}
	}
	return lines, tw, box
}
