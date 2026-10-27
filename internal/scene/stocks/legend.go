package stocks

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Stocks) Legend(s scene.Scene) scene.Legend {
	c := paletteOf(s.Dark)
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Glyph: "SECTOR", Color: c.headText.Hex(), Look: "a header bar over a sector panel of tickers; a chief's panel reads INDEX, with its goals' average price and line"},
			{Signal: scene.SigUnit, Glyph: "FLFM", Color: c.sym.Hex(), Look: "a ticker: a symbol made from the goal's name, then its name and price chart"},
			{Signal: scene.SigProgress, Glyph: "50.00", Color: c.text.Hex(), Look: "the price, 0 to 100; the price line ends at it"},
			{Signal: scene.SigBusy, Glyph: "▃▄▅▆▇█", Color: c.busy.Hex(), Look: "a volume bar pulsing between these heights under the line's end, one per agent, its name on the left"},
			{Signal: scene.SigWaiting, Glyph: "!!", Color: c.red.Hex(), Look: "a red alert tick among the volume bars, the agent's name in red"},
			{Signal: scene.SigResting, Glyph: "▁▁", Color: c.rest.Hex(), Look: "a flat grey volume bar"},
			{Signal: scene.SigPR, Glyph: "█", Color: c.open.Hex(), Look: "a candle right of the chart, taller the more gates it passed"},
			{Signal: scene.SigPRGreen, Glyph: "█", Color: c.green.Hex(), Look: "a green candle"},
			{Signal: scene.SigPRRed, Glyph: "█", Color: c.red.Hex(), Look: "a red candle"},
			{Signal: scene.SigMerged, Glyph: "⚑", Color: c.gold.Hex(), Look: "a full gold candle with a dividend flag over it"},
			{Signal: scene.SigDone, Glyph: "100.00", Color: c.text.Hex(), Look: "the price at 100.00, the line at the top of its chart"},
			{Signal: scene.SigNeedsYou, Glyph: "!! NEEDS YOU", Color: c.red.Hex(), Look: "a red alert on the ticker's top row, which flashes all red every other second"},
			{Signal: scene.SigSelected, Glyph: "▸▌", Color: c.sel.Hex(), Look: "a marker and bar down the ticker's left edge, its top row lit; a selected panel's header bar lit"},
			{Signal: scene.SigClear, Look: "the line trading normally, no badge"},
			{Signal: scene.SigClouds, Glyph: "FLAT", Color: c.dim.Hex(), Look: "flat trading: the line levels off, grey"},
			{Signal: scene.SigStorm, Glyph: "CRASH", Color: c.violet.Hex(), Look: "a crash: the line spikes and plunges in flickering violet"},
			{Signal: scene.SigEruption, Glyph: "HALTED", Color: c.halt.Hex(), Look: "trading halted: the line stops under a striped halt banner"},
			{Signal: scene.SigSummit, Glyph: "ATH✦", Color: c.gold.Hex(), Look: "an all-time high: a gold star with sparks at the line's end"},
			{Signal: scene.SigKill, Glyph: "F̶L̶F̶M̶", Color: c.dim.Hex(), Look: "delisted: the line flatlines to zero and the ticker strikes through, then fades"},
		},
		Decoration: []string{"the top bar's market session and clock (your clock)", "the ticker tape along the bottom",
			"the line's wander before now and the change after the price (seeded)", "the fill under the line", "grid dots"},
	}
}
