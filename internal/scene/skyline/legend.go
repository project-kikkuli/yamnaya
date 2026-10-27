package skyline

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Skyline) Legend(s scene.Scene) scene.Legend {
	pal := paletteAt(s.Now, s.Dark)
	red, green, gold := pal.red.Hex(), pal.green.Hex(), pal.gold.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Look: "a district of buildings, its name on the road below; a chief's nested districts stand on a podium a floor higher per level"},
			{Signal: scene.SigUnit, Look: "a building, planned taller the more agents and pull requests it has"},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the building's name; the finished floors rise with it, the rest is a bare steel frame"},
			{Signal: scene.SigBusy, Glyph: "♟", Color: pal.worker.Hex(), Look: "a named worker on the west face welding (✶ sparks), with a yellow tower crane on the roof while anyone is busy"},
			{Signal: scene.SigWaiting, Glyph: "♟", Color: red, Look: "a red worker standing still on the west face"},
			{Signal: scene.SigResting, Glyph: "♙ N resting", Look: "on the sidewalk at the building's foot"},
			{Signal: scene.SigPR, Glyph: "▤", Color: pal.label.Hex(), Look: "a floor in grey fit-out, higher the more gates it passed, its number on the east face"},
			{Signal: scene.SigPRGreen, Glyph: "▤", Color: green, Look: "a floor with its windows lit green"},
			{Signal: scene.SigPRRed, Glyph: "▤", Color: red, Look: "a dark red floor"},
			{Signal: scene.SigMerged, Glyph: "▣", Color: gold, Look: "a floor of finished gold facade; several share one, \"N merged\""},
			{Signal: scene.SigDone, Glyph: "✦", Color: gold, Look: "the tower complete, every window lit, a blinking antenna on top"},
			{Signal: scene.SigNeedsYou, Glyph: "◉", Color: red, Look: "a blinking red beacon on the roof, the name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Color: pal.rim.Hex(), Look: "over the building, its name lit and two searchlights crossing on it; a selected district's name reads ▸ name ◂"},
			{Signal: scene.SigClear, Look: "open sky"},
			{Signal: scene.SigClouds, Look: "brown smog round the upper floors"},
			{Signal: scene.SigStorm, Glyph: "╱", Color: pal.rain.Hex(), Look: "a dark cloud over the roof, rain and lightning"},
			{Signal: scene.SigEruption, Look: "a kaiju stomps beside the building breathing a beam at it, the top floors on fire, no workers"},
			{Signal: scene.SigSummit, Look: "a warm glow behind the lit tower"},
			{Signal: scene.SigKill, Look: "demolition: charges flash, the building implodes floor by floor into a dust cloud"},
		},
		Decoration: []string{"moon and stars (your clock)", "far towers", "window lights coming and going", "street lamps", "cars", "river and reflections", "building styles"},
	}
}
