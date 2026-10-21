package work_test

import (
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestMergeDismissalSurvivesStatusChangesAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	w, _ := s.Work.Ensure("fixture", "", "Caller migration")
	url := "https://github.com/example/query/pull/42"
	id := work.MergeReviewID(url)
	apply := func(e work.Event) {
		t.Helper()
		e.Work, e.Expected = w.ID, w.Revision
		w, err = s.Work.Commit(e)
		if err != nil {
			t.Fatal(err)
		}
	}
	status := func(version, state string) {
		apply(work.Event{Type: "merge-status", Review: work.Review{ID: id, URL: url, Action: "merge", Version: version, State: state}})
	}
	status("head1:ready", work.Ready)
	for _, actor := range []string{"manager", "GitHub", ""} {
		if _, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "dismiss-merge", Actor: actor, Target: id}); err == nil {
			t.Fatal("non-owner dismissed merge")
		}
	}
	apply(work.Event{Type: "dismiss-merge", Actor: "owner", Target: id})
	apply(work.Event{Type: "close"})
	for _, state := range []string{"waiting", work.Ready} {
		status("head2:"+state, state)
		r := w.Reviews[id]
		if !r.Dismissed || r.Pending() || r.QueueState() != "dismissed" || w.Ready() != 0 || r.Judgment != "" {
			t.Fatalf("dismissal lost: %+v", r)
		}
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, _ = s.Work.Get(w.ID)
	if !w.Reviews[id].Dismissed {
		t.Fatal("restart lost dismissal")
	}
	apply(work.Event{Type: "restore-merge", Actor: "owner", Target: id})
	if !w.Reviews[id].Pending() || w.Ready() != 1 {
		t.Fatal("restore failed")
	}
	apply(work.Event{Type: "dismiss-merge", Actor: "owner", Target: id})
	apply(work.Event{Type: "merge-status", Review: work.Review{ID: id, URL: url, Action: "merge", Version: "merged", State: work.Completed, Closed: true}})
	if r := w.Reviews[id]; r.QueueState() != work.Completed || !r.Closed || r.Judgment != "" {
		t.Fatalf("merge outcome lost: %+v", r)
	}
}

func TestOwnerDismissalDoesNotRaceWithIndependentStatusUpdates(t *testing.T) {
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, _ := s.Work.Ensure("fixture", "", "Caller migration")
	url := "https://github.com/example/query/pull/42"
	id := work.MergeReviewID(url)
	w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "merge-status", Review: work.Review{ID: id, URL: url, Action: "merge", Version: "head1", State: work.Ready}})
	if err != nil {
		t.Fatal(err)
	}
	viewed := w
	_, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "merge-status", Review: work.Review{ID: id, URL: url, Action: "merge", Version: "head2", State: "waiting"}})
	if err != nil {
		t.Fatal(err)
	}
	c := core.Core{Store: s}
	w, err = c.ChangeWork(viewed, work.Event{Type: "dismiss-merge", Actor: "owner", Target: id})
	if err != nil || !w.Reviews[id].Dismissed || w.Reviews[id].Version != "head2" || w.Reviews[id].State != "waiting" {
		t.Fatalf("dismissal raced with status %+v %v", w, err)
	}
}
