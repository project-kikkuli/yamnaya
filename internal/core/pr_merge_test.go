package core

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestMergeReadinessRequiresPositiveCurrentGitHubFacts(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*PRStatus)
		want bool
	}{
		{"approved and clean", func(*PRStatus) {}, true},
		{"no required reviews or CI", func(s *PRStatus) { s.Review, s.Passed = "", 0 }, true},
		{"draft", func(s *PRStatus) { s.Merge.Draft = true }, false},
		{"unknown head", func(s *PRStatus) { s.Merge.OID = "" }, false},
		{"review required", func(s *PRStatus) { s.Review = "REVIEW_REQUIRED" }, false},
		{"changes requested", func(s *PRStatus) { s.Review = "CHANGES_REQUESTED" }, false},
		{"failed checks", func(s *PRStatus) { s.Failed = 1 }, false},
		{"pending checks", func(s *PRStatus) { s.Pending = 1 }, false},
		{"unresolved thread", func(s *PRStatus) { s.Unresolved = 1 }, false},
		{"conflicts", func(s *PRStatus) { s.Merge.Mergeable = "CONFLICTING" }, false},
		{"blocked by policy", func(s *PRStatus) { s.Merge.MergeState = "BLOCKED" }, false},
		{"behind base", func(s *PRStatus) { s.Merge.MergeState = "BEHIND" }, false},
		{"unverified merge state", func(s *PRStatus) { s.Merge.MergeState = "" }, false},
		{"unknown status", func(s *PRStatus) { s.State = "UNKNOWN" }, false},
		{"source unavailable", func(s *PRStatus) { s.Merge.Error = "API quota exhausted" }, false},
		{"merged", func(s *PRStatus) { s.State = "MERGED" }, false},
		{"closed", func(s *PRStatus) { s.State = "CLOSED" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := PRStatus{State: "OPEN", Review: "APPROVED", Passed: 12, Merge: PRMergeFacts{OID: "head1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
			tc.edit(&s)
			if s.ReadyToMerge() != tc.want {
				t.Fatalf("readiness = %v, want %v: %+v", s.ReadyToMerge(), tc.want, s)
			}
		})
	}
}

func TestTrackedPRRefreshesAfterAgentExitAndRetainsItsLastHeadOnFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	url := "https://github.com/me/app/pull/2"
	w, err := s.Work.Ensure("fixture", "", "Caller migration")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "watch", Source: work.Source{Kind: "github", URL: url, Origin: "session-pr"}}); err != nil {
		t.Fatal(err)
	}
	src := &countingSource{calls: map[string]int{}, fail: map[string]bool{}}
	now := time.Now()
	c := Core{Store: s, PRs: src, Now: func() time.Time { return now }}
	if n, err := c.RefreshPRs(Board{}); err != nil || n != 1 || src.calls[url] != 1 {
		t.Fatalf("orphaned PR stopped refreshing: n=%d err=%v calls=%v", n, err, src.calls)
	}
	prior := PRStatus{State: "OPEN", Title: "Caller migration", Review: "APPROVED", Passed: 12, Fetched: now, Merge: PRMergeFacts{OID: "head1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	if err = s.SavePRStatus(url, prior); err != nil {
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
	got, err := s.PRStatuses()
	if err != nil || got[url].Merge != prior.Merge || !got[url].ReadyToMerge() {
		t.Fatalf("merge facts did not survive restart: %+v %v", got, err)
	}
	c.Store, now = s, now.Add(PRStale)
	src.fail[url] = true
	if _, err = c.RefreshPRs(Board{}); err == nil {
		t.Fatal("source failure was hidden")
	}
	got, _ = s.PRStatuses()
	if got[url].Merge.OID != "head1" || got[url].Merge.Error == "" || got[url].ReadyToMerge() || got[url].State != "UNKNOWN" {
		t.Fatalf("failed refresh lost the known head or claimed readiness: %+v", got[url])
	}
}

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
