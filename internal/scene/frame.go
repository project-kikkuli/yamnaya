package scene

import (
	"fmt"
	"strings"
	"sync"
	"time"

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
	p := still(s, width, height).at(frame, box)
	p.motion()
	return p.encode(lines, tw)
}

// light is how finely the time of day moves the sky: once per bucket, so the backdrop is reused in between.
const light = 10 * time.Minute

var backdrops struct {
	sync.Mutex
	key  string
	p    *painter
	hits int
}

// still is the backdrop for a scene at a size, kept while nothing that shapes it changes: the work drawn, the
// selection, the theme and the time of day to the bucket. The table plays no part in it.
func still(s Scene, width, height int) *painter {
	s.Now = s.Now.Truncate(light)
	key := fmt.Sprintf("%d %d %v %v %v %q %v", width, height, s.Dark, s.Now.Format(time.TimeOnly), s.Ranges, s.Selected, s.Peaks)
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.p != nil && backdrops.key == key {
		backdrops.hits++
		return backdrops.p
	}
	p := newPainter(s, width, height, 0)
	p.backdrop()
	backdrops.key, backdrops.p = key, p
	return p
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
	p := still(s, width, height).at(0, box)
	p.motion()
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

// place clips the table to the screen and sets it centered with its bottom edge a few rows above the screen's,
// growing upward, with a column of margin when there is room. Nothing about the scene depends on its height.
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
		bottom := height - min(3, height/24)
		y := max(0, bottom-len(lines))
		box = rect{x - pad, y, x + tw + pad, y + len(lines)}
	}
	return lines, tw, box
}
