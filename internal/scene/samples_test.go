package scene

import (
	"fmt"
	"strings"
)

type sample struct {
	name  string
	scene Scene
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

func caucasus(dark bool) Scene {
	return Scene{Dark: dark, Selected: "elbrus", Ranges: []Range{
		{ID: "rockies", Name: "Rockies", Peaks: []Peak{
			{ID: "robson", Name: "Robson", Weather: Clear, Climbers: 3, Progress: .6},
			{ID: "assiniboine", Name: "Assiniboine", Weather: Summit, Climbers: 2, Progress: 1},
			{ID: "temple", Name: "Temple", Weather: Clouds},
		}, Ranges: []Range{{ID: "bow", Name: "Bow", Peaks: []Peak{
			{ID: "bow1", Name: "Bow Peak", Weather: Clear, Climbers: 1, Progress: .3},
			{ID: "bow2", Name: "Crowfoot", Weather: Clear, Climbers: 2, Progress: .8},
		}}}},
		{ID: "caucasus", Name: "Caucasus", Peaks: []Peak{
			{ID: "elbrus", Name: "Elbrus", Weather: Storm, Climbers: 4, Progress: .45},
			{ID: "kazbek", Name: "Kazbek", Weather: Eruption, Climbers: 3, Progress: .5},
			{ID: "ushba", Name: "Ushba", Weather: Clear, Climbers: 5, Progress: .7},
		}},
	}}
}

func samples() []sample {
	many := Scene{Dark: true}
	for r := range 12 {
		rg := Range{ID: fmt.Sprint("r", r), Name: fmt.Sprint("range ", r)}
		for k := range 40 {
			w := []Weather{Clear, Clear, Clouds, Clear, Summit, Clear, Clouds, Clear}[(r*7+k*3)%8]
			if r == 4 && k == 17 {
				w = Eruption
			}
			if r == 9 && k%9 == 2 {
				w = Storm
			}
			rg.Peaks = append(rg.Peaks, Peak{ID: fmt.Sprint("p", r, "-", k), Weather: w, Climbers: k % 4, Progress: float64(k%10) / 10})
		}
		many.Ranges = append(many.Ranges, rg)
	}
	return []sample{
		{"dusk-160x48", caucasus(true), 160, 48, 30, board(12, 0)},
		{"day-160x48", caucasus(false), 160, 48, 30, board(12, 0)},
		{"dusk-80x24", caucasus(true), 80, 24, 30, board(8, 0)},
		{"dusk-260x70", caucasus(true), 260, 70, 30, board(14, 0)},
		{"crowd-200x50", many, 200, 50, 30, board(10, 0)},
		{"empty-120x36", Scene{Dark: true}, 120, 36, 30, board(6, 0)},
	}
}
