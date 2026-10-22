package core

import (
	"path/filepath"
	"testing"
	"time"
)

func TestMergeFactsUpgradePreservesPRsAndInvalidatesOpenStatuses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for _, state := range []string{"OPEN", "MERGED", "CLOSED"} {
		if err = s.SavePRStatus(state, PRStatus{State: state, Title: "Caller migration", Passed: 12, Fetched: now}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.db.Exec(`ALTER TABLE ym_prs DROP COLUMN merge_details`); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	statuses, err := s.PRStatuses()
	if err != nil || len(statuses) != 3 || !statuses["OPEN"].Fetched.IsZero() || statuses["MERGED"].Fetched.IsZero() || statuses["CLOSED"].Fetched.IsZero() || statuses["OPEN"].Passed != 12 {
		t.Fatalf("upgrade lost PRs or invalidated terminal states: %+v %v", statuses, err)
	}
}
