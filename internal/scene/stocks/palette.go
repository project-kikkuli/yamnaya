package stocks

import "github.com/project-kikkuli/yamnaya/internal/scene"

type rgb = scene.RGB

// palette is the screen's colors. Red is only ever a waiting agent, needs-you or failing CI.
type palette struct {
	bg, panel, grid, bar, barText, head, headText, kind, text, dim, sym, line, fill, idx    rgb
	busy, rest, red, redText, green, open, gold, violet, haze, sel, selText, halt, haltText rgb
}

var dark = palette{
	bg: scene.Hex("#050608"), panel: scene.Hex("#0b0e14"), grid: scene.Hex("#161b26"),
	bar: scene.Hex("#ff9d1c"), barText: scene.Hex("#000000"),
	head: scene.Hex("#1a1f2b"), headText: scene.Hex("#ffb627"), kind: scene.Hex("#8a92a6"),
	text: scene.Hex("#dfe3ea"), dim: scene.Hex("#7d8596"), sym: scene.Hex("#ffb627"),
	line: scene.Hex("#49c6ff"), fill: scene.Hex("#1b5a80"), idx: scene.Hex("#f4f4f4"),
	busy: scene.Hex("#8fb3ff"), rest: scene.Hex("#3f4655"),
	red: scene.Hex("#ff3b30"), redText: scene.Hex("#ffffff"),
	green: scene.Hex("#2fd06b"), open: scene.Hex("#c9ced8"), gold: scene.Hex("#ffd447"), violet: scene.Hex("#b98cff"),
	haze: scene.Hex("#2a2f3a"), sel: scene.Hex("#ffab2e"), selText: scene.Hex("#000000"),
	halt: scene.Hex("#ffc400"), haltText: scene.Hex("#000000"),
}

var light = palette{
	bg: scene.Hex("#ebe5d8"), panel: scene.Hex("#f7f3ea"), grid: scene.Hex("#e2dccf"),
	bar: scene.Hex("#f08a00"), barText: scene.Hex("#000000"),
	head: scene.Hex("#1f2633"), headText: scene.Hex("#ffb53d"), kind: scene.Hex("#a9b0bf"),
	text: scene.Hex("#1d2129"), dim: scene.Hex("#666b75"), sym: scene.Hex("#a14f00"),
	line: scene.Hex("#0a67c2"), fill: scene.Hex("#a9cbec"), idx: scene.Hex("#1d2129"),
	busy: scene.Hex("#2849b8"), rest: scene.Hex("#b3ad9f"),
	red: scene.Hex("#d0021b"), redText: scene.Hex("#ffffff"),
	green: scene.Hex("#15803d"), open: scene.Hex("#5f6672"), gold: scene.Hex("#9a6b00"), violet: scene.Hex("#7a3fc4"),
	haze: scene.Hex("#d9d3c6"), sel: scene.Hex("#f08a00"), selText: scene.Hex("#000000"),
	halt: scene.Hex("#ffc400"), haltText: scene.Hex("#000000"),
}

// paletteOf is the screen for the terminal; a trading screen lights itself, so the time of day shows only as
// the market session in the top bar.
func paletteOf(darkTerm bool) palette {
	if darkTerm {
		return dark
	}
	return light
}
