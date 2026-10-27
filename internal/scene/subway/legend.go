package subway

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Subway) Legend(s scene.Scene) scene.Legend {
	pl := paletteAt(s.Now, s.Dark)
	line := pl.lines[0].Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Glyph: "─", Look: "a network: a tinted district with the repository's name over its lines; a chief's network is marked ⇄"},
			{Signal: scene.SigUnit, Glyph: " A ", Color: line, Look: "a transit line in its own color, never red, with its lettered badge and name at the left end"},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the line's name; the thick track reaches that far and the rest is thin, still being built"},
			{Signal: scene.SigBusy, Glyph: "▐█▶ ◀█▌", Look: "a train running along the line, its nose the way it goes, white on dark lines and black on light ones"},
			{Signal: scene.SigWaiting, Glyph: "▐!▌", Color: pl.red.Hex(), Look: "a red train with a white rim, stopped on the line"},
			{Signal: scene.SigResting, Glyph: "▭N", Look: "trains parked in the depot, after the line's progress"},
			{Signal: scene.SigPR, Glyph: "○", Color: line, Look: "a station on the line, further from the badge the more gates it passed"},
			{Signal: scene.SigPRGreen, Glyph: "◉", Color: pl.green.Hex(), Look: "a station with a green light"},
			{Signal: scene.SigPRRed, Glyph: "✕", Color: pl.red.Hex(), Look: "a closed station, red"},
			{Signal: scene.SigMerged, Glyph: "●", Color: line, Look: "a station filled in the line's color, near the terminus"},
			{Signal: scene.SigDone, Glyph: "★", Color: pl.gold.Hex(), Look: "a gold star at the terminus: the line is open"},
			{Signal: scene.SigNeedsYou, Glyph: "!", Color: pl.red.Hex(), Look: "the terminus blinks red, the line's name in red"},
			{Signal: scene.SigSelected, Glyph: "▸", Color: pl.accent.Hex(), Look: "before the line's badge or network's name, the line lit and its name bright"},
			{Signal: scene.SigClear, Look: "a clean line, trains running"},
			{Signal: scene.SigClouds, Look: "delays: grey cloud drifts over a faded line"},
			{Signal: scene.SigStorm, Glyph: "ϟ", Color: pl.bolt.Hex(), Look: "a service disruption: dark cloud, rain ╱ and lightning over the line"},
			{Signal: scene.SigEruption, Glyph: "╳", Color: pl.amber.Hex(), Look: "the line suspended: broken grey track, barrier at its start, smoke, no trains running"},
			{Signal: scene.SigSummit, Glyph: "✦", Color: pl.gold.Hex(), Look: "the grand opening: gold sparkles round the terminus"},
			{Signal: scene.SigKill, Look: "the line closes: its stations dim and the track erases from the terminus back"},
		},
		Decoration: []string{"paper: cream by day, dark at night (your clock)", "river", "district tint", "rule after a network's name", "line colors (each line's own, no meaning)"},
	}
}
