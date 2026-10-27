package carrier

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Carrier) Legend(s scene.Scene) scene.Legend {
	lt := lightAt(s.Now, s.Dark)
	red, green, gold := lt.red.Hex(), lt.green.Hex(), lt.gold.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Glyph: "◥▀▀◤", Color: lt.hull.Hex(), Look: "an aircraft carrier, its name on the hull and the group's weather round its island; a chief is a fleet, its carriers in formation over a ╰─ name fleet ─╯ line, a nested chief's carrier among them; a crowded screen folds carriers into \"N carriers\""},
			{Signal: scene.SigUnit, Look: "a squadron: its own stretch of flight deck, named above it, ending at its catapult »; a crowded deck folds squadrons into \"N squadrons\""},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the squadron's name"},
			{Signal: scene.SigBusy, Glyph: busyA + busyB, Color: lt.crew.Hex(), Look: "a yellow-shirt deck crew member waving planes on"},
			{Signal: scene.SigWaiting, Glyph: busyA, Color: red, Look: "a red-shirt crew member standing still at the aft end of the squadron's deck"},
			{Signal: scene.SigResting, Glyph: rester, Color: scene.Mix(lt.deck, lt.plane, .6).Hex(), Look: "a gray crew member sitting on deck"},
			{Signal: scene.SigPR, Glyph: tail + body + nose, Color: lt.plane.Hex(), Look: "a plane on deck, nearer its catapult » the more gates it passed; \"+N\" over the deck counts planes with no room"},
			{Signal: scene.SigPRGreen, Glyph: tail + body + nose, Color: green, Look: "a green plane, cleared for the catapult"},
			{Signal: scene.SigPRRed, Glyph: tail + body + nose, Color: red, Look: "a red plane, grounded"},
			{Signal: scene.SigMerged, Glyph: tail + body + nose, Color: gold, Look: "a gold plane airborne over the squadron, darkened against a bright sky; \"+N\" counts more"},
			{Signal: scene.SigDone, Glyph: pennant, Color: gold, Look: "a gold pennant in place of the squadron's catapult"},
			{Signal: scene.SigNeedsYou, Glyph: "▚▞", Color: red, Look: "red signal flags blinking over the squadron, its name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Look: "before the squadron's, carrier's or fleet's name, the name lit"},
			{Signal: scene.SigClear, Look: "open sky over the deck"},
			{Signal: scene.SigClouds, Glyph: "░▒▓", Color: lt.cloud.Hex(), Look: "a low ceiling of cloud just over the deck"},
			{Signal: scene.SigStorm, Glyph: "╱ ^", Color: lt.rain.Hex(), Look: "heavy seas: rain over the deck, spray at the waterline, the deck rolling its planes and crew"},
			{Signal: scene.SigEruption, Glyph: "▲", Color: lt.fire.Hex(), Look: "a deck fire: flames and smoke, no crew but the red-shirts"},
			{Signal: scene.SigSummit, Glyph: nose + nose + nose, Look: "a flyover streaking over the deck"},
			{Signal: scene.SigKill, Look: "the lead plane is pushed overboard and falls past the hull into a splash"},
		},
		Decoration: []string{"sun, moon and stars (your clock)", "waves", "the carrier's island and its lit windows", "deck color"},
	}
}
