package mountains

import (
	"fmt"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

type sample struct {
	name  string
	scene scene.Scene
	w, h  int
	frame int
	table string
}

func board(rows, width int) string {
	var b strings.Builder
	b.WriteString("\x1b[1m" + pad("GOAL", 28) + pad("STATE", 12) + "AGENTS\x1b[0m\n")
	for i := range rows {
		b.WriteString(pad(fmt.Sprintf("goal %d: ship the thing", i), 28) + "\x1b[32m" + pad("running", 12) + "\x1b[0m" + fmt.Sprint(i%4) + "\n")
	}
	return b.String()
}

func pad(s string, n int) string { return s + strings.Repeat(" ", max(0, n-len(s))) }

func caucasus(dark bool) scene.Scene {
	return scene.Scene{Dark: dark, Selected: "elbrus", Ranges: []scene.Range{
		{ID: "rockies", Name: "Rockies", Peaks: []scene.Peak{
			{ID: "robson", Name: "Robson", Weather: scene.Clear, Team: team(3), Progress: .6},
			{ID: "assiniboine", Name: "Assiniboine", Weather: scene.Summit, Team: team(2), Progress: 1},
			{ID: "temple", Name: "Temple", Weather: scene.Clouds},
		}, Ranges: []scene.Range{{ID: "bow", Name: "Bow", Peaks: []scene.Peak{
			{ID: "bow1", Name: "Bow Peak", Weather: scene.Clear, Team: team(1), Progress: .3},
			{ID: "bow2", Name: "Crowfoot", Weather: scene.Clear, Team: team(2), Progress: .8},
		}}}},
		{ID: "caucasus", Name: "Caucasus", Peaks: []scene.Peak{
			{ID: "elbrus", Name: "Elbrus", Weather: scene.Storm, Team: team(4), Progress: .45},
			{ID: "kazbek", Name: "Kazbek", Weather: scene.Eruption, Team: team(3), Progress: .5},
			{ID: "ushba", Name: "Ushba", Weather: scene.Clear, Team: team(5), Progress: .7},
		}},
	}}
}

func samples() []sample {
	many := scene.Scene{Dark: true}
	for r := range 12 {
		rg := scene.Range{ID: fmt.Sprint("r", r), Name: fmt.Sprint("range ", r)}
		for k := range 40 {
			w := []scene.Weather{scene.Clear, scene.Clear, scene.Clouds, scene.Clear, scene.Summit, scene.Clear, scene.Clouds, scene.Clear}[(r*7+k*3)%8]
			if r == 4 && k == 17 {
				w = scene.Eruption
			}
			if r == 9 && k%9 == 2 {
				w = scene.Storm
			}
			rg.Peaks = append(rg.Peaks, scene.Peak{ID: fmt.Sprint("p", r, "-", k), Weather: w, Team: team(k % 4), Progress: float64(k%10) / 10})
		}
		many.Ranges = append(many.Ranges, rg)
	}
	goals := work()
	goals.Selected = "importer"
	goals.Ranges = append(goals.Ranges, scene.Range{ID: "repo:yamnaya", Name: "yamnaya", Peaks: []scene.Peak{
		{ID: "importer", Name: "make the board read like the range", Progress: .35, Team: []scene.Climber{{Name: "summit-mountains", Stance: scene.Climbing}, {Name: "summit-catcher", Stance: scene.Climbing}, {Name: "validator", Stance: scene.Resting}},
			Camps: []scene.Camp{{Label: "#88", State: scene.Green, Stage: 4}, {Label: "#91", State: scene.Open, Stage: 2}}},
		{ID: "teardown", Name: "retire the work layer", Weather: scene.Summit, Progress: 1, Camps: []scene.Camp{{Label: "#80", State: scene.Merged, Stage: 6}}},
	}})
	at := func(h, m int) scene.Scene {
		s := goals
		s.Now = time.Date(2026, 1, 15, h, m, 0, 0, time.Local)
		return s
	}
	return []sample{
		{"work-174x72", goals, 174, 72, 0, board(22, 0)},
		{"dawn-174x72", at(6, 40), 174, 72, 0, board(22, 0)},
		{"noon-174x72", at(11, 55), 174, 72, 0, board(22, 0)},
		{"golden-174x72", at(18, 0), 174, 72, 0, board(22, 0)},
		{"midnight-174x72", at(0, 30), 174, 72, 0, board(22, 0)},
		{"dusk-160x48", caucasus(true), 160, 48, 30, board(12, 0)},
		{"day-160x48", caucasus(false), 160, 48, 30, board(12, 0)},
		{"dusk-80x24", caucasus(true), 80, 24, 30, board(8, 0)},
		{"dusk-260x70", caucasus(true), 260, 70, 30, board(14, 0)},
		{"crowd-200x50", many, 200, 50, 30, board(10, 0)},
		{"empty-120x36", scene.Scene{Dark: true}, 120, 36, 30, board(6, 0)},
	}
}

// team is n busy agents named after the Caucasus guides who first climbed there.
func team(n int) []scene.Climber {
	names := []string{"Khergiani", "Gukasov", "Shalvi", "Tsereteli", "Abalakov", "Gvaliya"}
	out := make([]scene.Climber, n)
	for i := range out {
		out[i] = scene.Climber{Name: names[i%len(names)], Stance: scene.Climbing}
	}
	return out
}
