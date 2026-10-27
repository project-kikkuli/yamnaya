package scene

import "time"

// Step is how much animation time one frame stands for.
const Step = 250 * time.Millisecond

// Ambient is how often a scene's own life moves when no work does. Each change touches only some rows, so the
// terminal redraws little.
const Ambient = 2500 * time.Millisecond

// KillTime is the longest a kill animation plays before the unit leaves the scene.
const KillTime = 1500 * time.Millisecond

// Pace is how often the scene changes: every step while an eruption or a kill plays, once a second while agents
// work, rain falls or a flare signals the owner, else at the slow ambient beat; never under reduced motion.
func Pace(s Scene) time.Duration {
	if ReducedMotion {
		return 0
	}
	pace := time.Duration(0)
	s.Walk(func(p Peak) {
		switch {
		case p.Weather == Eruption || p.Dying > 0:
			pace = Step
		case pace == 0 && (p.NeedsYou || p.Weather == Storm || climbing(p.Team)):
			pace = time.Second
		}
	}, func(Range) {})
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
