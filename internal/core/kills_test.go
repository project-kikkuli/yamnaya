package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestKillJournalKeepsLatestConfirmedExitAcrossProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Unix(1000, 0)
	for _, entry := range []struct {
		id string
		at time.Time
	}{{"old", now.Add(-time.Hour)}, {"team", now.Add(-time.Second)}, {"team", now}} {
		if err := s.RecordKill(entry.id, entry.at); err != nil {
			t.Fatal(err)
		}
	}
	other, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for _, tc := range []struct {
		name string
		from time.Time
		want int
	}{{"recent", now.Add(-time.Minute), 1}, {"inclusive", now, 1}, {"future", now.Add(time.Nanosecond), 0}, {"history", now.Add(-2 * time.Hour), 2}} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := other.Kills(tc.from)
			if err != nil || len(got) != tc.want {
				t.Fatalf("kills %v, %v; want %d", got, err, tc.want)
			}
			if tc.want > 0 && !got["team"].Equal(now) {
				t.Fatalf("latest exit = %s; want %s", got["team"], now)
			}
		})
	}
}
