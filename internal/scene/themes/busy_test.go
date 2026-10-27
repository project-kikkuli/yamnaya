package themes

import (
	"fmt"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/scene"
)

// busy is a full board: 30 units in six repositories and a nested chief, 60 agents in every stance, 40 pull
// requests in every state and stage, every weather, flares, a selection and a kill playing.
func busy(theme string, dying float64) scene.Scene {
	s := scene.Scene{Theme: theme, Dark: true, Selected: "u7"}
	weathers := []scene.Weather{scene.Clear, scene.Clouds, scene.Storm, scene.Eruption, scene.Summit}
	unit := func(n int) scene.Peak {
		p := scene.Peak{ID: fmt.Sprint("u", n), Name: fmt.Sprint("ship unit ", n), Weather: weathers[n%5], Progress: float64(n%11) / 10, NeedsYou: n%6 == 0}
		for a := range 2 {
			p.Team = append(p.Team, scene.Climber{Name: fmt.Sprint("agent-", n, "-", a), Stance: scene.Stance((n + a) % 3)})
		}
		for c := range 2 {
			if k := n*2 + c; k < 40 && (n+c)%3 != 2 || n < 10 {
				p.Camps = append(p.Camps, scene.Camp{Label: fmt.Sprint("#", 100+k), State: scene.CampState((n + c) % 4), Stage: (n + c) % 7})
			}
		}
		if n == 12 {
			p.Dying = dying
		}
		return p
	}
	n := 0
	for r := range 5 {
		g := scene.Range{ID: fmt.Sprint("repo:", r), Name: fmt.Sprint("repo-", r)}
		for range 5 {
			g.Peaks = append(g.Peaks, unit(n))
			n++
		}
		s.Ranges = append(s.Ranges, g)
	}
	chief := scene.Range{ID: "chief", Name: "platform chief", Weather: scene.Clear, Ranges: []scene.Range{{ID: "sub", Name: "infra chief"}}}
	for range 3 {
		chief.Peaks = append(chief.Peaks, unit(n))
		n++
	}
	for range 2 {
		chief.Ranges[0].Peaks = append(chief.Ranges[0].Peaks, unit(n))
		n++
	}
	s.Ranges = append(s.Ranges, chief)
	return s
}

func table(rows int) string {
	var b strings.Builder
	b.WriteString("\x1b[1mGOAL                        STATE       AGENTS\x1b[0m\n")
	for i := range rows {
		fmt.Fprintf(&b, "goal %-23d \x1b[32mmoving\x1b[0m      %d\n", i, i%4)
	}
	return b.String()
}
