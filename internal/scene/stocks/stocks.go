// Package stocks draws the board as a trading screen: each repository is a sector panel, each chief an index
// of its goals, each unit a ticker whose price is its progress.
package stocks

import "github.com/project-kikkuli/yamnaya/internal/scene"

// Stocks draws each group as a sector panel and each unit as a ticker: busy agents trade as volume bars, pull
// requests are candles, and its weather is how the ticker trades.
type Stocks struct{}

func init() { scene.Register(Stocks{}) }

func (Stocks) Name() string { return "stocks" }

func (Stocks) Paint(s scene.Scene, width, height, frame int, box scene.Rect) *scene.Canvas {
	c := scene.NewCanvas(width, height, box)
	b := plan(s, width, height, box)
	p := painter{c: c, box: box, pal: paletteOf(s.Dark), frame: frame, plain: scene.Colors == scene.NoColor, sel: s.Selected, w: width, h: height}
	p.paint(s, b)
	return c
}

func (Stocks) Hit(s scene.Scene, width, height int, box scene.Rect, x, y int) string {
	for _, bl := range plan(s, width, height, box).blocks {
		if !bl.r.has(x, y) {
			continue
		}
		if bl.kind == strip {
			return bl.pn.units[bl.unit].ID
		}
		return bl.pn.id
	}
	return ""
}
