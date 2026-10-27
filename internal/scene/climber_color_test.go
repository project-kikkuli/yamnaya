package scene

import (
	"testing"
	"time"
)

func TestBusyAndWaitingClimbersDifferAtEveryHour(t *testing.T) {
	for _, dark := range []bool{true, false} {
		for minutes := 0; minutes < 24*60; minutes += 15 {
			now := time.Date(2026, 1, 1, minutes/60, minutes%60, 0, 0, time.UTC)
			th := themeAt(now, dark)
			dr, dg, db := th.climber.r-th.flag.r, th.climber.g-th.flag.g, th.climber.b-th.flag.b
			if dist := dr*dr + dg*dg + db*db; dist < .35*.35 {
				t.Fatalf("at %s (dark=%v) busy %v and waiting %v climbers are too alike", now.Format("15:04"), dark, th.climber, th.flag)
			}
		}
	}
}
