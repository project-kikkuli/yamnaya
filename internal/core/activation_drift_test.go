package core

import (
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/drift"
)

func TestDriftActionUsesTheRecordedCurrentEpisodeAndCanonicalOwner(t *testing.T) {
	at := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	sign := looping(at.Add(-10 * time.Minute))
	for _, tc := range []struct {
		name    string
		id      string
		current []drift.Sign
		crew    bool
		goal    string
		want    bool
	}{
		{name: "live worker", id: "worker", current: []drift.Sign{sign}, goal: "goal1", want: true},
		{name: "live crew", id: "worker/member", current: []drift.Sign{sign}, crew: true, goal: "goal1", want: true},
		{name: "recovered", id: "worker", goal: "goal1"},
		{name: "different episode", id: "worker", current: []drift.Sign{looping(at)}, goal: "goal1"},
		{name: "wrong canonical goal", id: "worker", current: []drift.Sign{sign}, goal: "goal2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := swollenTestBoard(at, nil)
			s := Session{ID: "worker"}
			if tc.crew {
				s.Crew = []Member{{ID: "member", Facts: Facts{Drift: tc.current}}}
			} else {
				s.Drift = tc.current
			}
			b.Sessions = []Live{{Session: s}}
			e := GoalEvent{Goal: tc.goal, Kind: EventAgentAdrift, Key: driftNoticeKey(tc.id, sign), Payload: "manager arbitrary-display-name looks adrift"}
			got, err := (&Core{}).driftActionNeeded(b, e)
			if err != nil || got != tc.want {
				t.Fatalf("needed %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
