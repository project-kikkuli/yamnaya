package kaiju

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Kaiju) Legend(s scene.Scene) scene.Legend {
	p := paletteAt(s.Now, s.Dark)
	red, gold := p.flare.Hex(), p.gold.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Color: p.dim.Hex(), Look: "a district of the harbor city, its blocks, pagodas and pylons on the skyline, named in the subtitles under the picture; a chief's nested districts a line above"},
			{Signal: scene.SigUnit, Look: "a monster black against the searchlights, bigger the more agents and pull requests it has; its id picks it for good from an atomic lizard, giant robot, steel lizard, giant moth (a crawling larva below 40%), three-headed gold dragon, giant turtle, pteranodon, giant ape, giant crab, sea serpent, ankylosaur and a lanky beast that shakes the camera"},
			{Signal: scene.SigProgress, Glyph: "NN%", Color: p.label.Hex(), Look: "after its name; it wades in from the far bay up to its chest at 0% and strides up the quay into the city as the work gets done"},
			{Signal: scene.SigBusy, Glyph: "► ◄", Color: p.jet.Hex(), Look: "a named fighter jet strafing round the monster's head, a searchlight crew on the quay holding a beam on it"},
			{Signal: scene.SigWaiting, Glyph: "✶ ◆", Color: red, Look: "a red tank halted on the quay below it, firing a red flare, its crew's name in red"},
			{Signal: scene.SigResting, Glyph: "▪ N parked", Color: p.dim.Hex(), Look: "crews parked below the monster"},
			{Signal: scene.SigPR, Glyph: "╥", Color: p.dim.Hex(), Look: "a barricade the army throws up in its path: on the beach for few gates passed, back among the buildings the more, its number beside it"},
			{Signal: scene.SigPRGreen, Glyph: "▲", Color: p.green.Hex(), Look: "a green missile battery, armed"},
			{Signal: scene.SigPRRed, Glyph: "▲", Color: red, Look: "a red battery on fire"},
			{Signal: scene.SigMerged, Glyph: "⚑", Color: gold, Look: "a gold flag on ground the monster has flattened and claimed; several share one, \"N merged\""},
			{Signal: scene.SigDone, Glyph: "終", Color: gold, Look: "the film's gold end card over the monster's head"},
			{Signal: scene.SigNeedsYou, Glyph: "◉ ◎", Color: red, Look: "a blinking red air-raid siren beside its name, the name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Color: p.jet.Hex(), Look: "over the monster, its name lit white and two searchlights crossing on it; a selected district's subtitle reads ▾ name"},
			{Signal: scene.SigClear, Look: "a clear night, only the searchlights sweeping"},
			{Signal: scene.SigClouds, Look: "smoke rolling across the monster"},
			{Signal: scene.SigStorm, Glyph: "╱", Look: "a black storm cloud over it, rain, and lightning that lights its outline white"},
			{Signal: scene.SigEruption, Look: "its plates glow blue-white and every few seconds its atomic breath scorches a line into the city, which bursts into flame and smoke"},
			{Signal: scene.SigSummit, Look: "it roars from the top of a ruined lattice tower, rings of sound rolling from its jaw"},
			{Signal: scene.SigKill, Look: "the monster sinks back under the sea in a burst of foam, or a robot buckles and falls into its own dust, in 1.5s"},
		},
		Decoration: []string{"silver film grain, flicker, scratches and dust", "letterbox and vignette", "searchlights sweeping the sky",
			"the moon and its path on the bay (your clock)", "lit windows", "the lattice tower", "the swell on the bay",
			"rare sights: a glowing sphere bubbling in the bay, a statue's head rolling down the quay, twin singers by the moth, a MONSTER ZERO marquee, a newspaper headline, handheld camera shake"},
	}
}
