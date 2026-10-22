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
	p := newPainter(s, width, height, frame, box)
	p.paint()
	return p.encode(lines, tw)
}
