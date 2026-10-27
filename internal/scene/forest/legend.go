package forest

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Forest) Legend(s scene.Scene) scene.Legend {
	pal := paletteAt(s.Now, s.Dark)
	red, green, blossom := pal.red.Hex(), pal.green.Hex(), pal.blossom.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Look: "a grove of trees on dark moss and ferns, its name on the ground below; a chief is a clearing of light meadow with a hedge at each side, and a nested chief's clearing rises on a knoll"},
			{Signal: scene.SigUnit, Look: "a tree, able to grow taller the more agents and pull requests it has"},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the tree's name; the tree grows from a sapling to its full height with it"},
			{Signal: scene.SigBusy, Glyph: "v ʌ ∂", Color: pal.agent.Hex(), Look: "a named bird flapping or a squirrel scurrying on the west of the trunk; pale yellow at night, dark blue by day"},
			{Signal: scene.SigWaiting, Glyph: "v", Color: red, Look: "a red bird sitting still on the trunk, its name in red"},
			{Signal: scene.SigResting, Glyph: "zz N resting", Look: "asleep in the roots"},
			{Signal: scene.SigPR, Glyph: "◡", Color: pal.nest.Hex(), Look: "a twig nest on the east of the trunk with its number, higher the more gates it passed"},
			{Signal: scene.SigPRGreen, Glyph: "◡", Color: green, Look: "a nest glowing green"},
			{Signal: scene.SigPRRed, Glyph: "◠", Color: red, Look: "a red, wilted nest turned over"},
			{Signal: scene.SigMerged, Glyph: "✿", Color: blossom, Look: "a blossom where the nest was; several share one, \"N merged\""},
			{Signal: scene.SigDone, Glyph: "✿", Color: blossom, Look: "a blossom on top of the tree at its full height"},
			{Signal: scene.SigNeedsYou, Glyph: "◆ ◇", Color: red, Look: "a blinking red lantern hung on a branch, the name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Color: pal.rim.Hex(), Look: "over the tree, its name lit and a shaft of light falling on it; a selected grove's name reads ▸ name ◂"},
			{Signal: scene.SigClear, Look: "open sky"},
			{Signal: scene.SigClouds, Look: "fog drifting round the trunk and lower crown"},
			{Signal: scene.SigStorm, Glyph: "╱", Color: pal.rain.Hex(), Look: "a dark cloud, slanting rain and lightning, the crown swaying in the wind"},
			{Signal: scene.SigEruption, Look: "wildfire: the crown burns, embers and smoke rise, the birds and squirrels have fled"},
			{Signal: scene.SigSummit, Look: "full bloom: the crown covered in blossom, petals falling, a warm glow behind"},
			{Signal: scene.SigKill, Look: "the tree is felled: it topples over, lands in a burst of leaves and fades"},
		},
		Decoration: []string{"sun, moon and stars (your clock)", "fireflies at night", "far hills of firs", "grass, fallen leaves and ferns", "meadow flowers", "tree kinds: broadleaf, fir and birch"},
	}
}
