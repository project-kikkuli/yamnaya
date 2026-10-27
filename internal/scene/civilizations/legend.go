package civilizations

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Civilizations) Legend(s scene.Scene) scene.Legend {
	lt := lightAt(s.Now, s.Dark)
	red, green, gold := lt.red.Hex(), lt.green.Hex(), lt.gold.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Glyph: dashH + dashV, Color: lt.border.Hex(), Look: "a continent named over its coast, its weather after the name; a chief is an empire, a dashed border round its continents with its name and weather on the top edge, and a nested chief's continent has a dashed border of its own inside it; a crowded map folds continents into \"N continents\""},
			{Signal: scene.SigUnit, Glyph: capital, Color: lt.lit.Hex(), Look: "a civilization: its capital on its own stretch of the continent, named above it; a crowded continent folds civilizations into \"N civs\""},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the civilization's name; its tinted territory, edged in a deeper tint, spreads east from the capital the more is done"},
			{Signal: scene.SigBusy, Glyph: unitG, Color: lt.busy.Hex(), Look: "a unit marching along the civilization's road, pale yellow at night, dark blue by day"},
			{Signal: scene.SigWaiting, Glyph: envoy, Color: red, Look: "a red envoy standing still at the capital's west gate"},
			{Signal: scene.SigResting, Glyph: rester + "N", Color: lt.rester.Hex(), Look: "a gray pawn under the capital, with how many when more than one"},
			{Signal: scene.SigPR, Glyph: "∘▪■▣", Color: lt.city.Hex(), Look: "a city off the road, a hamlet ∘ growing to a town ▪, a city ■ and a citadel ▣ the more gates it passed; a crowded civilization shows its burning cities first"},
			{Signal: scene.SigPRGreen, Glyph: "∘▪■▣", Color: green, Look: "a thriving green city"},
			{Signal: scene.SigPRRed, Glyph: "∘▪■▣", Color: red, Look: "a burning red city on embers"},
			{Signal: scene.SigMerged, Glyph: wonder, Color: gold, Look: "a gold wonder among the cities"},
			{Signal: scene.SigDone, Glyph: crown, Color: gold, Look: "a gold crown in place of the capital"},
			{Signal: scene.SigNeedsYou, Glyph: flag, Color: red, Look: "a red envoy flag blinking over the capital, the civilization's name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Look: "before the civilization's, continent's or empire's name, the name lit"},
			{Signal: scene.SigClear, Look: "open country"},
			{Signal: scene.SigClouds, Glyph: fog, Color: lt.fog.Hex(), Look: "stagnation: a gray fog of war over the civilization"},
			{Signal: scene.SigStorm, Glyph: swords, Color: lt.steel.Hex(), Look: "war or plague: a dark pall over the civilization, crossed swords clashing at the end of its road"},
			{Signal: scene.SigEruption, Glyph: rubbleA + rubbleB + ruin + "^", Color: lt.ruin.Hex(), Look: "sacked by barbarians: ash-gray territory strewn with ruins and fires, the capital a ruin Π"},
			{Signal: scene.SigSummit, Look: "a golden age: a gold border shimmering round the territory"},
			{Signal: scene.SigKill, Look: "the civilization falls: its cities are razed ∩, its capital a ruin Π, and its territory fades to rubble ▖▝ and bare land over 1.5s"},
		},
		Decoration: []string{"each civilization's tint (tells neighbors apart; means nothing)", "ocean depth and waves", "coasts", "unnamed islands", "forests ♣ and mountains ▲", "roads ─", "day and night shading (your clock)"},
	}
}
