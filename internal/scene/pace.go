package scene

import "time"

// Step is how much animation time one frame stands for.
const Step = 250 * time.Millisecond

// Pace is how often the scene changes: every step while an eruption plays, once a second while agents climb, rain
// falls or a flare signals the owner, else at the slow ambient beat; never under reduced motion.
func Pace(s Scene) time.Duration {
	if reducedMotion {
		return 0
	}
	pace := time.Duration(0)
	walk := func(p Peak) {
		switch {
		case p.Weather == Eruption:
			pace = Step
		case pace == 0 && (p.NeedsYou || p.Weather == Storm || climbing(p.Team)):
			pace = time.Second
		}
	}
	var visit func(r Range)
	visit = func(r Range) {
		for _, p := range r.Peaks {
			walk(p)
		}
		for _, sub := range r.Ranges {
			visit(sub)
		}
	}
	for _, r := range s.Ranges {
		visit(r)
	}
	for _, p := range s.Peaks {
		walk(p)
	}
	if pace == 0 {
		return Ambient
	}
	return pace
}

func climbing(team []Climber) bool {
	for _, c := range team {
		if c.Stance == Climbing {
			return true
		}
	}
	return false
}
