package islands

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Islands) Legend(s scene.Scene) scene.Legend {
	p := paletteAt(s.Now, s.Dark)
	red, gold := p.flare.Hex(), p.gold.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Color: p.dim.Hex(), Look: "an archipelago, named under it; a chief's islands ring a pale lagoon, its name in the middle"},
			{Signal: scene.SigUnit, Look: "an island, bigger the more agents and pull requests it has: sand rim, green interior, hills"},
			{Signal: scene.SigProgress, Glyph: "NN%", Color: p.label.Hex(), Look: "after the island's name; the island greens from its hills down to the beach as work gets done"},
			{Signal: scene.SigBusy, Glyph: "▸ ◂", Color: p.boat.Hex(), Look: "a named boat sailing round the island, its wake behind it"},
			{Signal: scene.SigWaiting, Glyph: "✶ ▸", Color: red, Look: "a red boat stopped off the south shore under a red flare"},
			{Signal: scene.SigResting, Glyph: "▪ N resting", Color: p.dim.Hex(), Look: "under the island's south shore"},
			{Signal: scene.SigPR, Glyph: "□", Color: p.open.Hex(), Look: "a building: a dock at the shore for few gates passed, further inland the more"},
			{Signal: scene.SigPRGreen, Glyph: "■", Color: p.green.Hex(), Look: "a lit green building"},
			{Signal: scene.SigPRRed, Glyph: "⌧", Color: red, Look: "a damaged red building"},
			{Signal: scene.SigMerged, Glyph: "♜", Color: gold, Look: "a gold lighthouse on the shore with a turning beam; several share one, \"N merged\""},
			{Signal: scene.SigDone, Glyph: "⚑", Color: p.rim.Hex(), Look: "a flag on the island's summit"},
			{Signal: scene.SigNeedsYou, Glyph: "✸ SOS", Color: red, Look: "a blinking red SOS flare over the island, its name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Color: p.rim.Hex(), Look: "over the island or archipelago, its name lit and the island's shore outlined"},
			{Signal: scene.SigClear, Look: "open sea and sun on the island"},
			{Signal: scene.SigClouds, Look: "white clouds drifting over the island, their shadows on the water"},
			{Signal: scene.SigStorm, Glyph: "╱ ϟ", Look: "a dark storm cell over the island, rain and lightning"},
			{Signal: scene.SigEruption, Look: "the island turns volcano: ash, a lava crater, lava running down its flanks, smoke and embers"},
			{Signal: scene.SigSummit, Glyph: "╤", Color: gold, Look: "a full harbor: gold piers all round the shore in golden shallows"},
			{Signal: scene.SigKill, Look: "the sea rises over the island and it sinks, rings rippling out, in 1.5s"},
		},
		Decoration: []string{"ocean depth gradient", "swell and wave crests (~)", "surf on the shoals", "beaches and hills",
			"lagoon color", "sun or moon glint on the water (your clock)"},
	}
}
