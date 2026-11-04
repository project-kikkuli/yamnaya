package progress

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestOnlyASessionThatEndedCountsAsFinished(t *testing.T) {
	for _, c := range []struct {
		status   core.Status
		finished int
	}{
		{core.Busy, 0},
		{core.Waiting, 0},
		{core.Onboarding, 0},
		{core.Done, 1},
	} {
		t.Run(c.status.String(), func(t *testing.T) {
			var crew Crew
			crew.add(c.status, false)
			if crew.Finished != c.finished {
				t.Fatalf("finished %d, want %d", crew.Finished, c.finished)
			}
		})
	}
}
