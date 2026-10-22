package core

import (
	"testing"
	"time"
)

type dormantLaunches struct {
	sends
	dormant []Dormant
}

func (l *dormantLaunches) Dormant() []Dormant { return l.dormant }

func TestOwnerStopHidesSessionsUntilTheyRunAgain(t *testing.T) {
	stop := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		live    bool
		at      time.Time
		started time.Time
		shown   bool
	}{
		{"live process seen at the stop", true, stop, time.Time{}, false},
		{"live process seen after the stop", true, stop.Add(time.Second), time.Time{}, true},
		{"dormant launch stopped after it started", false, stop.Add(time.Hour), stop.Add(-time.Hour), false},
		{"dormant launch revived after the stop", false, stop.Add(time.Hour), stop.Add(time.Minute), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			if tc.live {
				w.run(42, 0, "/dev/a", "claude", "lead", Idle)
			} else {
				w.core.Launches = &dormantLaunches{dormant: []Dormant{{Key: "k", Harness: "claude", Session: "lead", Started: tc.started}}}
			}
			if err := w.core.Store.RecordKill("lead", stop); err != nil {
				t.Fatal(err)
			}
			w.core.Now = func() time.Time { return tc.at }
			shown := false
			for _, p := range w.board(t).Projects {
				for _, s := range p.Sessions {
					shown = shown || s.Key() == "lead"
				}
				for _, d := range p.Dormant {
					shown = shown || d.ID() == "lead"
				}
			}
			if shown != tc.shown {
				t.Fatalf("shown = %v, want %v", shown, tc.shown)
			}
		})
	}
}
