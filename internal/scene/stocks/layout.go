package stocks

import (
	"slices"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type rect struct{ x0, y0, x1, y1 int }

func (r rect) has(x, y int) bool { return x >= r.x0 && x < r.x1 && y >= r.y0 && y < r.y1 }

// panel is a sector of tickers: a repository, a chief's index, or the sessions that belong to neither.
type panel struct {
	id, kind, title string
	sky             scene.Weather
	index           bool
	units           []scene.Peak
	// members are the goals an index averages, its nested chiefs' included.
	members []scene.Peak
	hidden  int
}

type kind int

const (
	head kind = iota
	index
	strip
)

// block is one piece of a panel placed on the screen: its header bar, its index chart, or one ticker.
type block struct {
	kind kind
	r    rect
	pn   *panel
	unit int
	cont bool
}

type board struct {
	blocks []block
	// rows is how tall a ticker is: 1 is a one-line quote, 3 and up a chart.
	rows int
}

const (
	maxRows   = 8
	chartCol  = 40
	quoteCol  = 22
	indexRows = 2
)

// plan lays the panels out in newspaper columns above the table and beside it, with tickers as tall as fit;
// when even one-line quotes do not, the quietest tickers are left out, never the selection or an alert.
func plan(s scene.Scene, width, height int, box scene.Rect) board {
	pns := panelsOf(s)
	for rows := maxRows; rows >= 3; rows-- {
		for k := 1; k <= max(1, width/chartCol); k++ {
			if bl, ok := flow(pns, columns(width, height, box, k, chartCol), rows); ok {
				return board{blocks: bl, rows: rows}
			}
		}
	}
	for k := 1; k <= max(1, width/quoteCol); k++ {
		if bl, ok := flow(pns, columns(width, height, box, k, quoteCol), 1); ok {
			return board{blocks: bl, rows: 1}
		}
	}
	cols := columns(width, height, box, max(1, width/quoteCol), quoteCol)
	pns = clonePanels(pns)
	for _, drop := range quietest(pns, s.Selected) {
		pn := pns[drop.panel]
		i := slices.IndexFunc(pn.units, func(p scene.Peak) bool { return p.ID == drop.id })
		pn.units = slices.Delete(pn.units, i, i+1)
		pn.hidden++
		shown := pns[:0:0]
		for _, q := range pns {
			if len(q.units) > 0 || q.id != "" && q.id == s.Selected || q.index && q.hidden == 0 {
				shown = append(shown, q)
			}
		}
		if bl, ok := flow(shown, cols, 1); ok {
			return board{blocks: bl, rows: 1}
		}
	}
	return board{rows: 1}
}

func panelsOf(s scene.Scene) []*panel {
	var out []*panel
	var visit func(r scene.Range, crumbs string)
	visit = func(r scene.Range, crumbs string) {
		title := strings.ToUpper(r.Name)
		if crumbs != "" {
			title = crumbs + " / " + title
		}
		pn := &panel{id: r.ID, kind: "SECTOR", title: title, sky: r.Sky(), units: r.Peaks}
		if !strings.HasPrefix(r.ID, "repo:") {
			pn.kind, pn.index = "INDEX", true
			scene.Scene{Ranges: []scene.Range{r}}.Walk(func(p scene.Peak) { pn.members = append(pn.members, p) }, func(scene.Range) {})
		}
		out = append(out, pn)
		for _, sub := range r.Ranges {
			visit(sub, title)
		}
	}
	for _, r := range s.Ranges {
		visit(r, "")
	}
	if len(s.Peaks) > 0 {
		var parts []scene.Weather
		for _, p := range s.Peaks {
			parts = append(parts, p.Weather)
		}
		out = append(out, &panel{kind: "OTC", title: "SESSIONS", sky: scene.Combine(scene.Clear, parts), units: s.Peaks})
	}
	return out
}

func clonePanels(pns []*panel) []*panel {
	out := make([]*panel, len(pns))
	for i, pn := range pns {
		q := *pn
		q.units = slices.Clone(pn.units)
		out[i] = &q
	}
	return out
}

type unitRef struct {
	panel int
	id    string
}

// quietest is every ticker but the selected one and those with an alert, the calmest and latest first.
func quietest(pns []*panel, selected string) []unitRef {
	type ranked struct {
		unitRef
		rank, order int
	}
	var all []ranked
	for pi, pn := range pns {
		for _, p := range pn.units {
			r := 0
			if p.Weather == scene.Storm || p.Weather == scene.Eruption {
				r = 1
			}
			if p.Dying > 0 {
				r = 2
			}
			if p.NeedsYou || slices.ContainsFunc(p.Team, func(c scene.Climber) bool { return c.Stance == scene.Waiting }) || p.ID == selected {
				continue
			}
			all = append(all, ranked{unitRef{pi, p.ID}, r, len(all)})
		}
	}
	slices.SortStableFunc(all, func(a, b ranked) int {
		if a.rank != b.rank {
			return a.rank - b.rank
		}
		return b.order - a.order
	})
	out := make([]unitRef, len(all))
	for i, a := range all {
		out[i] = a.unitRef
	}
	return out
}

// columns is where panels flow: k columns across the screen above the table, then the strips left and right of
// it. The top row is the market bar and the bottom row the tape.
func columns(width, height int, box scene.Rect, k, minW int) []rect {
	var cols []rect
	split := func(r rect, n int) {
		if r.y1-r.y0 < 1 || r.x1-r.x0 < minW {
			return
		}
		n = max(1, min(n, (r.x1-r.x0)/minW))
		for i := range n {
			cols = append(cols, rect{r.x0 + (r.x1-r.x0)*i/n, r.y0, r.x0 + (r.x1-r.x0)*(i+1)/n, r.y1})
		}
	}
	bottom := height - 1
	if box.X1 <= box.X0 || box.Y1 <= box.Y0 {
		split(rect{0, 1, width, bottom}, k)
		return cols
	}
	split(rect{0, 1, width, box.Y0}, k)
	split(rect{0, box.Y0, box.X0, bottom}, box.X0*2/(3*minW))
	split(rect{box.X1, box.Y0, width, bottom}, (width-box.X1)*2/(3*minW))
	return cols
}

// flow places each panel's header, index and tickers down the columns in turn; a ticker never splits, and a
// panel that runs on into the next column repeats its header there.
func flow(pns []*panel, cols []rect, rows int) ([]block, bool) {
	var out []block
	if len(cols) == 0 {
		return nil, len(pns) == 0
	}
	ci, y := 0, cols[0].y0
	gap := 1
	if rows == 1 {
		gap = 0
	}
	fit := func(need int, spaced bool) bool {
		for ci < len(cols) {
			g := 0
			if spaced && y > cols[ci].y0 {
				g = gap
			}
			if y+g+need <= cols[ci].y1 {
				y += g
				return true
			}
			ci++
			if ci < len(cols) {
				y = cols[ci].y0
			}
		}
		return false
	}
	for _, pn := range pns {
		idx := 0
		if pn.index && rows > 1 {
			idx = indexRows
		}
		need := 1 + idx
		if len(pn.units) > 0 {
			need += rows
		}
		if !fit(need, true) {
			return out, false
		}
		col := cols[ci]
		out = append(out, block{kind: head, r: rect{col.x0, y, col.x1, y + 1}, pn: pn})
		y++
		if idx > 0 {
			out = append(out, block{kind: index, r: rect{col.x0, y, col.x1, y + idx}, pn: pn})
			y += idx
		}
		for i := range pn.units {
			if y+rows > cols[ci].y1 {
				ci++
				if ci >= len(cols) {
					return out, false
				}
				y = cols[ci].y0
				if !fit(1+rows, false) {
					return out, false
				}
				col = cols[ci]
				out = append(out, block{kind: head, r: rect{col.x0, y, col.x1, y + 1}, pn: pn, cont: true})
				y++
			}
			out = append(out, block{kind: strip, r: rect{col.x0, y, col.x1, y + rows}, pn: pn, unit: i})
			y += rows
		}
	}
	return out, true
}
