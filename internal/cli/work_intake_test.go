package cli

import (
	"context"
	"fmt"
	"path/filepath"
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
		return []byte(`{"title":"Migration","headRefOid":"abc","updatedAt":"2026-01-01T00:00:00Z","state":"OPEN"}`), nil
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
