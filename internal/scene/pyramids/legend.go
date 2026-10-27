package pyramids

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Pyramids) Legend(s scene.Scene) scene.Legend {
	pal := paletteAt(s.Now, s.Dark)
	red, gold := pal.red.Hex(), pal.gold.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Look: "a necropolis on the sand, its name below its pyramids; a chief is a royal complex a paved causeway links, its nested necropolises raised on a plateau a step higher per level"},
			{Signal: scene.SigUnit, Look: "a pyramid, planned steeper the more agents and pull requests it has"},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the pyramid's name; the stone courses rise with it, the courses still to come a dotted outline"},
			{Signal: scene.SigBusy, Glyph: "♟", Color: pal.worker.Hex(), Look: "a named worker on the west ramp hauling a sledge of stone (▪) up behind them"},
			{Signal: scene.SigWaiting, Glyph: "♟", Color: red, Look: "a red worker standing still on the west ramp, no sledge"},
			{Signal: scene.SigResting, Glyph: "♙ N resting", Look: "on the sand at the pyramid's foot"},
			{Signal: scene.SigPR, Glyph: "■", Color: pal.label.Hex(), Look: "a stone block on the east ramp, higher the more gates it passed, its number beside it"},
			{Signal: scene.SigPRGreen, Glyph: "■", Color: pal.green.Hex(), Look: "a block set green on the east ramp"},
			{Signal: scene.SigPRRed, Glyph: "▚", Color: red, Look: "a cracked red block on the east ramp"},
			{Signal: scene.SigMerged, Glyph: "▣", Color: pal.casing.Hex(), Look: "a block placed in the face, its course cased in white limestone; several share one, \"N merged\""},
			{Signal: scene.SigDone, Glyph: "✦", Color: gold, Look: "the gold capstone (pyramidion) gleaming on top, the ramps cleared away"},
			{Signal: scene.SigNeedsYou, Glyph: "✹", Color: red, Look: "a blinking red beacon fire at the head of the west ramp, the name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Color: pal.rim.Hex(), Look: "over the apex, its name lit and a shaft of light on it; a selected necropolis reads ▸ name ◂"},
			{Signal: scene.SigClear, Look: "open sky"},
			{Signal: scene.SigClouds, Look: "heat haze: a pale shimmer bending the air round the pyramid"},
			{Signal: scene.SigStorm, Glyph: "~", Color: mix(pal.storm, pal.haze, .35).Hex(), Look: "a sandstorm: a wall of brown sand blowing across the pyramid"},
			{Signal: scene.SigEruption, Look: "collapse: the upper courses fallen into rubble round the foot, stones still sliding, dust rising, no workers"},
			{Signal: scene.SigSummit, Look: "a golden glow in the sky behind the capped pyramid"},
			{Signal: scene.SigKill, Look: "the pyramid crumbles into a sand slide and sinks into the desert"},
		},
		Decoration: []string{"sun, moon and stars (your clock), and which face the sun lights", "dunes", "the Nile, its reeds and glints", "palms", "a felucca sailing", "stone texture"},
	}
}
