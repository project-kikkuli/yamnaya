package mountains

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Mountains) Legend(s scene.Scene) scene.Legend {
	th := themeAt(s.Now, s.Dark)
	red, green, gold := th.flag.Hex(), th.green.Hex(), th.gold.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Look: "a range of mountains; a chief's range stands nearer, in front of the ranges it holds"},
			{Signal: scene.SigUnit, Look: "a peak, taller the more agents and pull requests it has"},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the peak's name; the lead climber stands higher the more is done"},
			{Signal: scene.SigBusy, Glyph: "λ", Look: "a named climber on the west face; pale yellow at night, dark blue by day"},
			{Signal: scene.SigWaiting, Glyph: "λ", Color: red, Look: "a red climber"},
			{Signal: scene.SigResting, Glyph: "▴ N resting", Look: "at the west foot"},
			{Signal: scene.SigPR, Glyph: "▲", Look: "a camp on the east face, higher the more gates it passed"},
			{Signal: scene.SigPRGreen, Glyph: "▲", Color: green, Look: "a green camp"},
			{Signal: scene.SigPRRed, Glyph: "▲", Color: red, Look: "a red camp"},
			{Signal: scene.SigMerged, Glyph: "⚑", Color: gold, Look: "a gold flag at the camp line; several collapse to \"N merged\""},
			{Signal: scene.SigDone, Glyph: "⚑", Color: red, Look: "a flag on the summit"},
			{Signal: scene.SigNeedsYou, Glyph: "│ ✸", Color: red, Look: "a blinking red flare over the peak, its name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Look: "over the peak or range, its name lit"},
			{Signal: scene.SigClear, Look: "open sky"},
			{Signal: scene.SigClouds, Look: "torn cloud wrapped round the flanks"},
			{Signal: scene.SigStorm, Glyph: "╱", Look: "dark cloud over the summit, rain and lightning"},
			{Signal: scene.SigEruption, Look: "lava, smoke and embers, no climbers"},
			{Signal: scene.SigSummit, Look: "glow and halo round the peak"},
			{Signal: scene.SigKill, Look: "an avalanche of snow pours down the peak and buries it"},
		},
		Decoration: []string{"sun, moon and stars (your clock)", "lake", "pines", "fog", "wisps", "drifting clouds", "far ridges", "rim light"},
	}
}
