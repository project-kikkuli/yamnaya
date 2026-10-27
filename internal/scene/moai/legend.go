package moai

import "github.com/project-kikkuli/yamnaya/internal/scene"

func (Moai) Legend(s scene.Scene) scene.Legend {
	lt := lightAt(s.Now, s.Dark)
	red, green, scoria := lt.red.Hex(), lt.green.Hex(), lt.scoria.Hex()
	return scene.Legend{
		Entries: []scene.Entry{
			{Signal: scene.SigGroup, Glyph: "▟▆▆▙", Color: lt.ahu.Hex(), Look: "an ahu, a stone platform with the group's name cut in it and a cove of sea at its foot; a chief is a ceremonial site, its ahu side by side over a ╰─ name site ─╯ line, a nested chief's ahu among them; a crowded screen folds ahu into \"N ahu\""},
			{Signal: scene.SigUnit, Look: "a moai standing on the ahu, named above it; a crowded ahu folds moai into \"N moai\""},
			{Signal: scene.SigProgress, Glyph: "NN%", Look: "after the moai's name; the carving emerges as it grows: a rough block under a third, the face under two thirds, then the whole figure"},
			{Signal: scene.SigBusy, Glyph: busyA + busyB, Color: lt.carver.Hex(), Look: "a yellow carver beside the moai swinging an adze, stone chips flying"},
			{Signal: scene.SigWaiting, Glyph: busyA, Color: red, Look: "a red carver standing still at the moai's left"},
			{Signal: scene.SigResting, Glyph: rester, Color: lt.resting.Hex(), Look: "a gray carver sitting at the right end of the moai's place"},
			{Signal: scene.SigPR, Glyph: stone, Color: lt.rock.Hex(), Look: "a stone on the grass below the ahu, further right the more gates it passed; \"+N\" counts stones with no room"},
			{Signal: scene.SigPRGreen, Glyph: stone, Color: green, Look: "a green stone, set in place"},
			{Signal: scene.SigPRRed, Glyph: cracked, Color: red, Look: "a red cracked stone"},
			{Signal: scene.SigMerged, Glyph: pukao, Color: scoria, Look: "a pukao, a topknot of brown-orange scoria (red is reserved), at the right end of the stones"},
			{Signal: scene.SigDone, Glyph: topknot + topknot + topknot, Color: scoria, Look: "the pukao raised onto the finished moai's head"},
			{Signal: scene.SigNeedsYou, Glyph: fireA + fireB + fireBase, Color: red, Look: "a blinking red signal fire at the moai's left, its name in red"},
			{Signal: scene.SigSelected, Glyph: "▾", Look: "before the moai's, ahu's or site's name, the name lit"},
			{Signal: scene.SigClear, Look: "open sky over the moai"},
			{Signal: scene.SigClouds, Glyph: "░▒", Color: lt.mist.Hex(), Look: "sea mist drifting over the moai's head and the grass"},
			{Signal: scene.SigStorm, Glyph: "╱^*", Color: lt.rain.Hex(), Look: "rain over the moai and storm surf crashing in the cove below"},
			{Signal: scene.SigEruption, Look: "the moai toppled face down on its ahu in a cloud of dust, only red carvers left"},
			{Signal: scene.SigSummit, Look: "the whole moai stands on its ahu facing inland, white coral eyes set and its pukao on"},
			{Signal: scene.SigKill, Look: "the moai leans, topples forward off the ahu and falls into the cove with a splash"},
		},
		Decoration: []string{"sun, moon, stars and the Milky Way (your clock)", "grassy hills and tufts", "the sea and its waves", "the cove's waves", "the grain of the uncarved stone"},
	}
}
