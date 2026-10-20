package work_test

import (
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestReviewReceiptSurvivesRestartAndReturnsWhenEvidenceOrDirectionChanges(t *testing.T) {
	for _, change := range []string{"source", "context", "authority", "purpose"} {
		t.Run(change, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "ym.db")
			s := open(t, path)
			w := create(t, s)
			w = commit(t, s, w, work.Event{Type: "observe", Review: work.Review{ID: "doc", URL: "https://example.test/design", Title: "Permission proposal", Version: "v1", Order: 1}})
			w = commit(t, s, w, work.Event{Type: "reviewed", Actor: "owner", Target: "doc", Review: w.Reviews["doc"]})
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			s = open(t, path)
			defer s.Close()
			w, err := s.Work.Get(w.ID)
			if err != nil {
				t.Fatal(err)
			}
			r := w.Reviews["doc"]
			if r.Pending() || r.State != work.Reviewed || r.ReviewedVersion != "v1" || r.Judgment != "" || r.PreparedVersion != "" || r.NextOwner("") != "none" {
				t.Fatalf("receipt became an unfulfilled decision or lost its version: %+v", r)
			}
			w = commit(t, s, w, work.Event{Type: "observe", Review: work.Review{ID: "doc", Version: "v1", Order: 1}})
			if w.Reviews["doc"].Pending() {
				t.Fatal("unchanged polling revived a completed checklist item")
			}
			if change == "source" {
				w = commit(t, s, w, work.Event{Type: "observe", Review: work.Review{ID: "doc", Version: "v2", Order: 2}})
			} else {
				w = commit(t, s, w, work.Event{Type: change, Actor: "owner", Text: "Check both API and SDK"})
			}
			if !w.Reviews["doc"].Pending() || w.Reviews["doc"].ReviewedVersion != "v1" {
				t.Fatal("new evidence did not return with its previous receipt preserved")
			}
		})
	}
}

func TestChecklistRequiresTheOwnerAndCurrentPersonalEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, actor, state, version string
		provisional, closed         bool
		context                     int64
		wantErr                     bool
	}{
		{name: "incoming without manager", actor: "owner", state: "incoming", version: "v1"},
		{name: "preparing", actor: "owner", state: work.Preparing, version: "v1"},
		{name: "ready", actor: "owner", state: work.Ready, version: "v1"},
		{name: "agent cannot check off owner work", actor: "manager", state: "incoming", version: "v1", wantErr: true},
		{name: "stale source", actor: "owner", state: "incoming", version: "old", wantErr: true},
		{name: "stale context", actor: "owner", state: "incoming", version: "v1", context: 1, wantErr: true},
		{name: "provisional agent PR", actor: "owner", state: "incoming", version: "v1", provisional: true, wantErr: true},
		{name: "closed", actor: "owner", state: "incoming", version: "v1", closed: true, wantErr: true},
		{name: "awaiting author", actor: "owner", state: work.WithAuthor, version: "v1", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := work.Scope{Reviews: map[string]work.Review{"doc": {ID: "doc", Version: "v1", State: tc.state, Provisional: tc.provisional, Closed: tc.closed}}}
			err := work.Apply(&w, work.Event{Type: "reviewed", Actor: tc.actor, Target: "doc", Review: work.Review{Version: tc.version}, Context: tc.context})
			if (err != nil) != tc.wantErr {
				t.Fatalf("review receipt = %+v, err %v", w.Reviews["doc"], err)
			}
		})
	}
}

func TestChecklistReceiptsMergeUnrelatedChangesButRejectChangedEvidence(t *testing.T) {
	for _, change := range []string{"message", "source", "context"} {
		t.Run(change, func(t *testing.T) {
			s := open(t, filepath.Join(t.TempDir(), "ym.db"))
			defer s.Close()
			c := core.Core{Store: s}
			w := create(t, s)
			w = commit(t, s, w, work.Event{Type: "observe", Review: work.Review{ID: "doc", Version: "v1", Order: 1}})
			if change == "source" {
				commit(t, s, w, work.Event{Type: "observe", Review: work.Review{ID: "doc", Version: "v2", Order: 2}})
			} else {
				commit(t, s, w, work.Event{Type: change, Text: "Changed input"})
			}
			_, err := c.ChangeWork(w, work.Event{Type: "reviewed", Actor: "owner", Target: "doc"})
			if (err == nil) != (change == "message") {
				t.Fatalf("concurrent %s: %v", change, err)
			}
		})
	}
}
