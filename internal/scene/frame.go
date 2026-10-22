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
	key := fmt.Sprintf("%d %d %d %v %v %v %q %v", width, height, frame, s.Dark, s.Now, s.Ranges, s.Selected, s.Peaks)
	painted.Lock()
	if painted.key == key && painted.table == table {
		out := painted.out
		painted.Unlock()
		return out
	}
	painted.Unlock()
	lines, tw, box := place(table, width, height)
	p := still(s, width, height).at(frame, box)
	p.motion()
	out := p.encode(lines, tw)
	painted.Lock()
	painted.key, painted.table, painted.out = key, table, out
	painted.Unlock()
	return out
}

// painted is the last frame drawn, returned as is when nothing that shapes it changed.
var painted struct {
	sync.Mutex
	key, table, out string
}

// light is how finely the time of day moves the sky: once per bucket, so the backdrop is reused in between.
const light = 10 * time.Minute

var backdrops struct {
	sync.Mutex
	key  string
	p    *painter
	builds int
	// lit holds, per mountain, the pixels its selection light changes on the backdrop.
	lit map[int][]pixel
}

// still is the backdrop for a scene at a size with the selected mountain lit, the work and selection laid out
// on it for the frame to draw. The backdrop is kept while nothing it paints changes: the shape of the ranges,
// their weather, the theme and the time of day to the bucket. The table plays no part in it.
func still(s Scene, width, height int) *painter {
	s.Now = s.Now.Truncate(light)
	l := plan(s, width)
	bare := unlit(l)
	key := fmt.Sprintf("%d %d %v %v %v", width, height, s.Dark, s.Now.Format(time.TimeOnly), shape(bare))
	backdrops.Lock()
	defer backdrops.Unlock()
	if backdrops.p == nil || backdrops.key != key {
		backdrops.builds++
		p := blankPainter(s, width, height, 0)
		p.l = bare
		p.backdrop()
		backdrops.key, backdrops.p, backdrops.lit = key, p, map[int][]pixel{}
		go warm(key, s, width, height, bare)
	}
	q := *backdrops.p
	q.l = l
	for i, m := range l.mounts {
		if !m.selected {
			continue
		}
		lit, ok := backdrops.lit[i]
		if !ok {
			lit = lighting(backdrops.p, s, width, height, bare, i)
			backdrops.lit[i] = lit
		}
		q.lit = lit
	}
	return &q
}

// warm works out every mountain's selection light ahead of the keys that move the selection to it.
func warm(key string, s Scene, width, height int, bare layout) {
	backdrops.Lock()
	base := backdrops.p
	backdrops.Unlock()
	for i := range bare.mounts {
		backdrops.Lock()
		_, done := backdrops.lit[i]
		stale := backdrops.key != key
		backdrops.Unlock()
		if stale {
			return
		}
		if done {
			continue
		}
		lit := lighting(base, s, width, height, bare, i)
		backdrops.Lock()
		if backdrops.key == key {
			backdrops.lit[i] = lit
		}
		backdrops.Unlock()
	}
}

// lighting is the pixels that differ from the bare backdrop when mountain i is the selected one.
func lighting(base *painter, s Scene, width, height int, bare layout, i int) []pixel {
	l := bare
	l.mounts = append([]mount(nil), bare.mounts...)
	l.mounts[i].selected = true
	p := blankPainter(s, width, height, 0)
	p.l = l
	p.backdrop()
	var out []pixel
	for k, c := range p.px {
		if c != base.px[k] {
			out = append(out, pixel{k, c})
		}
	}
	return out
}

// unlit is a layout with no mountain selected, the way the backdrop is painted.
func unlit(l layout) layout {
	l.mounts = append([]mount(nil), l.mounts...)
	for i := range l.mounts {
		l.mounts[i].selected = false
	}
	return l
}

// shape is everything about a layout the backdrop paints: where each mountain and range stands, how tall and
// in what weather; not its names, climbers or camps, which are drawn on top each frame.
func shape(l layout) string {
	var b strings.Builder
	fmt.Fprint(&b, l.depth)
	for _, m := range l.mounts {
		fmt.Fprintf(&b, "|%x %v %v %v %d %d", m.seed, m.cx, m.half, m.lift, m.depth, m.weather)
	}
	for _, m := range l.massifs {
		fmt.Fprintf(&b, "|%x %v %v %d %d", m.seed, m.x0, m.x1, m.depth, m.weather)
	}
	return b.String()
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
