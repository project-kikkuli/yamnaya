package anthill

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Anthill) Legend(s scene.Scene) scene.Legend {
	pal := paletteAt(s.Now, s.Dark)
	red := pal.red.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Look: "a colony: its mounds linked by a gallery underground, its name under the gallery; a chief is a supercolony whose colonies' galleries run deeper and rise to join its own"},
			{Signal: scene.SigUnit, Look: "a mound on the surface over its nest, its name above the entrance"},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the mound's name; the mound stands taller and the brood comb under it spreads wider and deeper"},
			{Signal: scene.SigBusy, Glyph: "ж", Color: pal.ant.Hex(), Look: "an amber ant marching up and down the shaft and along the gallery"},
			{Signal: scene.SigWaiting, Glyph: "ж", Color: red, Look: "a red ant standing still beside the top of the shaft, its name beside it in red"},
			{Signal: scene.SigResting, Glyph: "ж N resting", Color: pal.rest.Hex(), Look: "a grey ant curled in the brood comb"},
			{Signal: scene.SigPR, Glyph: "○", Color: pal.open.Hex(), Look: "an empty chamber off the shaft, deeper the more gates it passed, its number beside it"},
			{Signal: scene.SigPRGreen, Glyph: "●", Color: pal.green.Hex(), Look: "a stocked chamber glowing green"},
			{Signal: scene.SigPRRed, Glyph: "▒", Color: red, Look: "a collapsed chamber full of red rubble"},
			{Signal: scene.SigMerged, Glyph: "◆", Color: pal.gold.Hex(), Look: "a chamber filled with gold food and sealed off from the shaft; several share one, \"N sealed\""},
			{Signal: scene.SigDone, Glyph: "ψ", Color: pal.alate.Hex(), Look: "the mound at full height, winged alates flying off it"},
			{Signal: scene.SigNeedsYou, Glyph: "Ж", Color: red, Look: "a big red ant blinking at the mound's entrance, red alarm rings pulsing out, the name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Color: pal.rim.Hex(), Look: "over the entrance, the name lit; a selected colony's name reads ▸ name ◂"},
			{Signal: scene.SigClear, Look: "open sky over the mound"},
			{Signal: scene.SigClouds, Look: "a haze of brown dust drifting over the mound"},
			{Signal: scene.SigStorm, Glyph: "╱", Color: pal.rain.Hex(), Look: "a rain cloud over the mound, flood water seeping down the shaft into the chambers and gallery"},
			{Signal: scene.SigEruption, Look: "an anteater beside the mound, its snout in the entrance and its pink tongue raiding down the shaft"},
			{Signal: scene.SigSummit, Look: "a warm glow round the full mound"},
			{Signal: scene.SigKill, Look: "the mound is flattened into a puff of dust and its nest and tunnels cave in"},
		},
		Decoration: []string{"sun, moon and stars (your clock)", "grass", "soil layers", "roots", "pebbles", "the brood comb's cells"},
	}
}
