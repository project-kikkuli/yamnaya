package core

import (
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTheBoardSaysWhenPullRequestReadsAreSlowedStoppedOrFailing runs one ym serve cycle at a share of the account's budget left,
// then reads the board in another process's store, as the TUI does, and checks the line it shows about pull request reads.
func TestTheBoardSaysWhenPullRequestReadsAreSlowedStoppedOrFailing(t *testing.T) {
	for _, c := range []struct {
		name  string
		left  float64
		down  bool
		after time.Duration
		want  string
	}{
		{"plenty", 0.9, false, 0, ""},
		{"slowed", 0.3, false, 0, "pull requests read every 16m: GitHub budget 30% left until"},
		{"board at the slowest", 0.2, false, 0, "pull requests read every 30m, goals' every 23m: GitHub budget 20% left until"},
		{"stopped", 0.05, false, 0, "pull request reads stopped: GitHub budget 5% left until"},
		{"failing", 0.9, true, 0, "pull request reads failing: GraphQL budget exhausted"},
		{"budget reset since", 0.05, false, 3 * time.Hour, ""},
		{"failure long past", 0.9, true, time.Hour, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym.db")
			st, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { st.Close() })
			now := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
			src := &budgetSource{batchSource: batchSource{now: func() time.Time { return now }, down: c.down}, left: c.left, reset: now.Add(2 * time.Hour), reads: map[string]int{}}
			serve := &Core{Store: st, PRs: src, Now: func() time.Time { return now }}
			b := Board{Sessions: []Live{{Session: Session{ID: "own", Facts: Facts{PRs: []PR{{URL: "https://github.com/acme/app/pull/1"}}}}}}}
			serve.RefreshPRs(b)
			view, err := OpenStore(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { view.Close() })
			pace, err := view.PRPace()
			if err != nil {
				t.Fatal(err)
			}
			got := pace.Note(now.Add(c.after))
			if c.want == "" && got != "" || !strings.HasPrefix(got, c.want) {
				t.Fatalf("note %q, want %q", got, c.want)
			}
		})
	}
}
