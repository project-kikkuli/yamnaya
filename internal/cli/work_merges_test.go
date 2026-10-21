package cli

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestMergeObligationSurvivesReviewCompletionAndStatusFailures(t *testing.T) {
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &core.Core{Store: s}
	w, err := s.Work.Ensure("fixture", "", "Caller migration")
	if err != nil {
		t.Fatal(err)
	}
	url := "https://github.com/example/query/pull/42"
	apply := func(e work.Event) {
		t.Helper()
		e.Work, e.Expected = w.ID, w.Revision
		w, err = s.Work.Commit(e)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(work.Event{Type: "watch", Source: work.Source{Kind: "github", URL: url, Origin: "session-pr", Requester: "Mira"}})
	apply(work.Event{Type: "observe", Review: work.Review{ID: url, URL: url, Title: "Caller migration", Requester: "Mira", Version: "head1:content", Order: 1}})
	apply(work.Event{Type: "close"})
	st := core.PRStatus{State: "OPEN", Title: "Caller migration", Passed: 12, Review: "APPROVED", Fetched: time.Now(), Merge: core.PRMergeFacts{Author: "mira", Viewer: "mira", OID: "head1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	capture := func() {
		t.Helper()
		if err := s.SavePRStatus(url, st); err != nil {
			t.Fatal(err)
		}
		if err := captureMergeReadiness(c); err != nil {
			t.Fatal(err)
		}
		w, err = s.Work.Get(w.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	id := work.MergeReviewID(url)
	capture()
	if w.Reviews[id].State != work.Ready || w.Reviews[id].Action != "merge" {
		t.Fatal("a ready PR in archived agent work did not create a merge obligation")
	}
	before := w.Revision
	capture()
	if w.Revision != before {
		t.Fatal("unchanged GitHub facts repeatedly notified the manager")
	}
	w, err = c.ChangeWork(w, work.Event{Type: "reviewed", Actor: "owner", Target: url})
	if err != nil || w.Reviews[id].State != work.Ready {
		t.Fatal("review completion hid a pending merge")
	}
	if _, err = c.ChangeWork(w, work.Event{Type: "reviewed", Actor: "owner", Target: id}); err == nil {
		t.Fatal("a local checklist receipt completed a GitHub merge")
	}
	apply(work.Event{Type: "context", Actor: "owner", Text: "Keep callers compatible"})
	capture()
	if w.Reviews[id].State != work.Ready {
		t.Fatal("context changed without a GitHub status change and lost the merge task")
	}
	st.State, st.Merge.Error = "UNKNOWN", "GitHub quota exhausted"
	capture()
	if w.Reviews[id].State != work.Ready || w.Reviews[id].StatusError == "" || w.Reviews[id].Question == "Merge this pull request?" {
		t.Fatal("an unavailable source silently lost a ready merge or claimed it was still verified")
	}
	before = w.Revision
	capture()
	if w.Revision != before {
		t.Fatal("unchanged coverage failure repeatedly changed the merge obligation")
	}
	st.State, st.Merge.Error, st.Pending = "OPEN", "", 1
	st.Merge.OID = "head2"
	capture()
	if w.Reviews[id].Pending() || w.Reviews[id].State != "waiting" {
		t.Fatal("a new head with unfinished CI remained marked ready")
	}
	st.Pending = 0
	capture()
	if w.Reviews[id].State != work.Ready {
		t.Fatal("newly green head did not return to the queue")
	}
	st.State = "MERGED"
	capture()
	if !w.Reviews[id].Closed || w.Reviews[id].State != work.Completed || w.Reviews[id].Judgment != "" {
		t.Fatal("GitHub merge did not complete its own obligation")
	}
	if !w.Reviews[url].Pending() || w.Reviews[url].ReviewedVersion != "head1:content" {
		t.Fatal("the merge lifecycle overwrote the independent review receipt")
	}
}

func TestAgentPRLinkRetainsExplicitRegistrationAndPriorReview(t *testing.T) {
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	c := &core.Core{Store: s}
	w, _ := s.Work.Ensure("fixture", "", "Caller migration")
	url := "https://github.com/example/query/pull/42"
	for _, e := range []work.Event{
		{Type: "bind", Target: "native"},
		{Type: "watch", Source: work.Source{Kind: "github", URL: url, Requester: "Mira", Question: "Keep the compatibility path?", Coverage: "explicitly registered source"}},
		{Type: "observe", Review: work.Review{ID: url, URL: url, Version: "head1", Order: 1, Requester: "Mira"}},
		{Type: "reviewed", Actor: "owner", Target: url, Review: work.Review{Version: "head1"}},
	} {
		e.Work, e.Expected = w.ID, w.Revision
		w, err = s.Work.Commit(e)
		if err != nil {
			t.Fatal(err)
		}
	}
	b := core.Board{Sessions: []core.Live{{Session: core.Session{ID: "native", Facts: core.Facts{PRs: []core.PR{{URL: url}}}}}}}
	if err = captureSessionPRs(c, b); err != nil {
		t.Fatal(err)
	}
	w, _ = s.Work.Get(w.ID)
	if !w.Sources[0].LinkedPR() || w.Sources[0].Requester != "Mira" || w.Sources[0].Question != "Keep the compatibility path?" || w.Reviews[url].State != work.Reviewed {
		t.Fatalf("linking agent work rewrote the human review: %+v", w)
	}
	w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "watch", Actor: "owner", Source: work.Source{Kind: "github", URL: url, Requester: "Mira", Question: "Keep the compatibility path?", Coverage: "explicitly registered source"}})
	if err != nil || !w.Sources[0].LinkedPR() {
		t.Fatal("updating the explicit registration lost the agent PR after its session exited")
	}
	st := core.PRStatus{State: "OPEN", Merge: core.PRMergeFacts{Author: "mira", Viewer: "mira", OID: "head1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	if err = s.SavePRStatus(url, st); err != nil {
		t.Fatal(err)
	}
	if err = captureMergeReadiness(c); err != nil {
		t.Fatal(err)
	}
	w, _ = s.Work.Get(w.ID)
	if w.Reviews[work.MergeReviewID(url)].State != work.Ready {
		t.Fatal("an explicitly watched PR linked to agent work had no merge task")
	}
}

func TestAutomaticMergeIntakeRequiresVerifiedAccountAuthorship(t *testing.T) {
	for _, tc := range []struct {
		name, author, viewer string
		want                 bool
	}{
		{"owner", "mira", "mira", true}, {"case insensitive", "Mira", "mira", true},
		{"mentioned colleague PR", "sol", "mira", false}, {"missing author", "", "mira", false}, {"missing viewer", "mira", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			c := &core.Core{Store: s}
			w, _ := s.Work.Ensure("fixture", "", "Caller migration")
			url := "https://github.com/example/query/pull/42"
			id := work.MergeReviewID(url)
			w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "watch", Source: work.Source{Kind: "github", URL: url, Origin: "session-pr"}})
			if err != nil {
				t.Fatal(err)
			}
			w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "merge-status", Review: work.Review{ID: id, URL: url, Action: "merge", State: work.Ready, Version: "legacy"}})
			if err != nil {
				t.Fatal(err)
			}
			st := core.PRStatus{State: "OPEN", Merge: core.PRMergeFacts{Author: tc.author, Viewer: tc.viewer, OID: "head1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
			if err = s.SavePRStatus(url, st); err != nil {
				t.Fatal(err)
			}
			if err = captureMergeReadiness(c); err != nil {
				t.Fatal(err)
			}
			w, _ = s.Work.Get(w.ID)
			if w.Reviews[id].Pending() != tc.want || w.Ready() != map[bool]int{true: 1, false: 0}[tc.want] {
				t.Fatalf("unexpected obligation %+v", w.Reviews[id])
			}
		})
	}
}
