package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/intake"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestSourceReconciliationBoundsParallelRequestsAndEventuallyVisitsEverySource(t *testing.T) {
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.Work.Ensure("fixture", "/src/query/.git", "Review intake")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "watch", Source: work.Source{Kind: "github", URL: fmt.Sprintf("https://github.com/example/query/pull/%d", i+1)}})
		if err != nil {
			t.Fatal(err)
		}
	}
	c := &core.Core{Store: s, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	var calls, active, maximum atomic.Int32
	release := make(chan struct{})
	f := intake.Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		if calls.Add(1) == 4 {
			close(release)
		}
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []byte(`{"title":"Migration","head":{"sha":"abc"},"updated_at":"2026-01-01T00:00:00Z","state":"open"}`), nil
	}}
	for i, want := range []int32{16, 20, 20} {
		if err = workCycle(context.Background(), c, f); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != want {
			t.Fatalf("cycle %d issued %d requests, want %d", i, calls.Load(), want)
		}
	}
	if maximum.Load() > 4 || maximum.Load() < 2 {
		t.Fatalf("source concurrency: %d", maximum.Load())
	}
	latest, err := s.Work.Get(w.ID)
	if err != nil || len(latest.Reviews) != 20 {
		t.Fatalf("lost sources: %d %v", len(latest.Reviews), err)
	}
}

func TestLinkedPRDiscoveredInMultipleMissionsHasOneAutomaticObligation(t *testing.T) {
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	url := "https://github.com/example/query/pull/42"
	var b core.Board
	for _, session := range []string{"first", "second"} {
		w, err := s.Work.Ensure(session, "/src/query/.git", session)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "bind", Target: session}); err != nil {
			t.Fatal(err)
		}
		b.Sessions = append(b.Sessions, core.Live{Session: core.Session{ID: session, Facts: core.Facts{PRs: []core.PR{{URL: url}}}}})
	}
	c := &core.Core{Store: s}
	for i := 0; i < 2; i++ {
		if err = captureSessionPRs(c, b); err != nil {
			t.Fatal(err)
		}
	}
	all, err := s.Work.List()
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, w := range all {
		count += len(w.Sources)
	}
	if count != 1 {
		t.Fatalf("same linked PR registered %d times", count)
	}
}

func TestGitHubAssignmentReconciliationPreservesExplicitAndActedOnWork(t *testing.T) {
	for _, tc := range []struct {
		name, origin, coverage, action string
		active, fail                   bool
		wantSources                    int
		wantState                      string
	}{
		{name: "team-only legacy import", coverage: "explicit GitHub review-request assignment", wantState: "withdrawn"},
		{name: "withdrawn direct assignment", origin: "github-request", wantState: "withdrawn"},
		{name: "direct assignment", origin: "github-request", active: true, wantSources: 1, wantState: "incoming"},
		{name: "explicit registration", wantSources: 1, wantState: "incoming"},
		{name: "explicit registration also assigned", active: true, wantSources: 1, wantState: "incoming"},
		{name: "owner feedback", origin: "github-request", action: "feedback", wantSources: 1, wantState: work.WithAuthor},
		{name: "owner decision", origin: "github-request", action: "decide", wantSources: 1, wantState: work.Completed},
		{name: "linked agent PR", origin: "session-pr", wantSources: 1, wantState: "incoming"},
		{name: "linked agent PR directly assigned", origin: "session-pr", active: true, wantSources: 1, wantState: "incoming"},
		{name: "coverage failure", origin: "github-request", fail: true, wantSources: 1, wantState: "incoming"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			w, err := s.Work.Intake()
			if err != nil {
				t.Fatal(err)
			}
			address := "https://github.com/example/query/pull/241"
			source := work.Source{Origin: tc.origin, Kind: "github", URL: address, Coverage: tc.coverage, Requester: "GitHub review assignment to reviewer", RequestVersion: "first", Question: "Original question"}
			apply := func(e work.Event) {
				t.Helper()
				e.Work, e.Expected = w.ID, w.Revision
				w, err = s.Work.Commit(e)
				if err != nil {
					t.Fatal(err)
				}
			}
			apply(work.Event{Type: "watch", Source: source})
			apply(work.Event{Type: "observe", Review: work.Review{ID: address, URL: address, Version: "head", Order: 1, Evidence: "Original evidence", Requester: source.Requester}})
			if tc.action != "" {
				apply(work.Event{Type: "prepare", Actor: "manager", Target: address, Review: work.Review{Version: "head", Question: "Accept?", Recommendation: "Accept", Evidence: "Test results"}})
				apply(work.Event{Type: tc.action, Actor: "owner", Target: address, Text: "Owner's recorded response"})
			}
			before := w.Reviews[address]
			if err = s.Work.SourceError(w.ID, address, fmt.Errorf("old refresh failure")); err != nil {
				t.Fatal(err)
			}
			f := intake.Fetcher{Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
				if tc.fail {
					return nil, fmt.Errorf("API unavailable")
				}
				if args[1] == "user" {
					return []byte("reviewer"), nil
				}
				if args[len(args)-1] == "repos/example/query/pulls/241/requested_reviewers" {
					return []byte(`{"users":[{"login":"reviewer"}]}`), nil
				}
				if tc.active {
					return []byte(`[{"total_count":1,"items":[{"updated_at":"second","pull_request":{"html_url":"https://github.com/example/query/pull/241"}}]}]`), nil
				}
				return []byte(`[{"total_count":0,"items":[]}]`), nil
			}}
			if err = captureRequests(context.Background(), &core.Core{Store: s}, f); (err != nil) != tc.fail {
				t.Fatalf("capture error = %v", err)
			}
			w, err = s.Work.Get(w.ID)
			if err != nil {
				t.Fatal(err)
			}
			r := w.Reviews[address]
			if len(w.Sources) != tc.wantSources || r.State != tc.wantState || r.Version != before.Version || r.Evidence != before.Evidence || r.Feedback != before.Feedback || r.Judgment != before.Judgment {
				t.Fatalf("reconciliation lost work: sources=%+v review=%+v", w.Sources, r)
			}
			if tc.origin == "" && tc.coverage == "" && w.Sources[0] != source {
				t.Fatalf("explicit registration overwritten: %+v", w.Sources)
			}
			if tc.origin == "session-pr" && !tc.active && (!r.Provisional || w.Sources[0].Requester != "" || !w.Sources[0].LinkedPR()) {
				t.Fatalf("linked PR lost its provenance: %+v %+v", w.Sources, r)
			}
			if !tc.fail && (tc.origin == "github-request" || tc.coverage != "" || tc.origin == "session-pr") && !tc.active {
				coverage, err := s.Work.Coverage(w.ID)
				if err != nil || len(coverage) != 0 {
					t.Fatalf("withdrawn request still reports stale errors: %v %v", coverage, err)
				}
			}
			if tc.wantSources == 0 {
				apply(work.Event{Type: "watch", Source: work.Source{Origin: "github-request", Kind: "github", URL: address, Requester: "GitHub review assignment to reviewer", RequestVersion: "first"}})
				if w.Reviews[address].State != "incoming" {
					t.Fatal("a fresh direct request did not reactivate the retained review identity")
				}
			}
		})
	}
}

type intakeHarness struct{ core.Harness }

func (intakeHarness) Name() string { return "fixture" }
func (intakeHarness) Discover([]proc.Proc) []core.Session {
	return []core.Session{{Harness: "fixture", ID: "fixture-session", Status: core.Idle, Facts: core.Facts{PRs: []core.PR{{URL: "https://github.com/example/query/pull/241"}}}}}
}

type intakePRStatus func(string) (core.PRStatus, error)

func (f intakePRStatus) PRStatus(url string) (core.PRStatus, error) { return f(url) }

func TestMissionStatusFailureDoesNotStopReviewEvidenceRefresh(t *testing.T) {
	s, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	w, err := s.Work.Intake()
	if err != nil {
		t.Fatal(err)
	}
	address := "https://github.com/example/query/pull/241"
	w, err = s.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "watch", Source: work.Source{Kind: "github", URL: address, Requester: "Review owner"}})
	if err != nil {
		t.Fatal(err)
	}
	c := &core.Core{Store: s, Harnesses: []core.Harness{intakeHarness{}}, Procs: func() ([]proc.Proc, error) { return nil, nil }, PRs: intakePRStatus(func(string) (core.PRStatus, error) {
		return core.PRStatus{}, fmt.Errorf("GraphQL quota exhausted")
	})}
	f := intake.Fetcher{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"title":"Latest evidence","head":{"sha":"abc"},"updated_at":"2026-01-01T00:00:00Z","state":"open"}`), nil
	}}
	if err = workCycle(context.Background(), c, f); err == nil || !strings.Contains(err.Error(), "GraphQL quota exhausted") {
		t.Fatalf("status failure disappeared: %v", err)
	}
	w, err = s.Work.Get(w.ID)
	if err != nil || w.Reviews[address].Title != "Latest evidence" {
		t.Fatalf("independent REST evidence did not refresh: %+v %v", w.Reviews, err)
	}
}
