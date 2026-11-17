package core

import (
	"testing"
	"time"
)

func TestSwollenActionUsesCurrentProcessGenerationAndOwnership(t *testing.T) {
	at := time.Date(2026, 11, 17, 9, 0, 30, 0, time.UTC)
	original := Swollen{PID: 5000, Session: "worker", Goal: "goal1", Elapsed: 51 * time.Minute, Share: 0.41}
	event := GoalEvent{Goal: "goal1", Kind: EventAgentSwollen, Created: at, Key: swollenNoticeKey(at, original), Payload: "memory: script pid 5000 holds 26 GiB"}
	for _, tc := range []struct {
		name    string
		current []Swollen
		at      time.Time
		want    bool
	}{
		{name: "same condition", current: []Swollen{original}, at: at, want: true},
		{name: "recovered", at: at},
		{name: "different process", current: []Swollen{{PID: 5001, Session: "worker", Goal: "goal1", Elapsed: original.Elapsed}}, at: at},
		{name: "reused pid", current: []Swollen{{PID: 5000, Session: "worker", Goal: "goal1", Elapsed: time.Minute}}, at: at.Add(time.Hour)},
		{name: "different goal", current: []Swollen{{PID: 5000, Session: "worker", Goal: "goal2", Elapsed: original.Elapsed}}, at: at},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Core{}
			got, err := c.swollenActionNeeded(Board{At: tc.at, Machine: Machine{Swollen: tc.current}}, event)
			if err != nil || got != tc.want {
				t.Fatalf("needed %v, %v; want %v", got, err, tc.want)
			}
		})
	}
	started := at.Truncate(time.Minute)
	fresh := Swollen{PID: 5000, Session: "worker", Goal: "goal1", Elapsed: time.Second}
	old := event
	old.Created = started.Add(5 * time.Second)
	old.Key = swollenNoticeKey(started.Add(20*time.Second), fresh)
	got, err := (&Core{}).swollenActionNeeded(Board{At: started.Add(20 * time.Second), Machine: Machine{Swollen: []Swollen{fresh}}}, old)
	if err != nil || got {
		t.Fatalf("same-minute recycled pid reused old action: %v, %v", got, err)
	}
}

func TestUnpreparedKeepsManagedContextAndExemptsStandaloneItems(t *testing.T) {
	for _, tc := range []struct {
		name string
		it   Item
		want bool
	}{
		{name: "managed without preparer", it: Item{GoalOwner: "goal1"}, want: true},
		{name: "managed historical dossier", it: Item{Dossier: Dossier{Goal: "goal1"}}, want: true},
		{name: "explicit standalone", it: Item{}},
		{name: "ready managed", it: Item{GoalOwner: "goal1", DoLine: "choose a", Rec: "a", Options: picks}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			it := tc.it
			it.Kind = KindDecision
			it.State = ItemOpen
			it.Recipient = Owner
			it.Question = "which?"
			if got := len(Unprepared(it)) > 0; got != tc.want {
				t.Fatalf("marked %v, want %v", got, tc.want)
			}
		})
	}
}
