package handoff_test

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

func TestSemanticCheckpointAndFailuresRemainDurableWithoutDuplicatingClaims(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	s, err := core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	events := []handoff.Event{{ID: "one", Work: "work", Text: "Review #42"}, {ID: "two", Work: "work", Text: "Choose a cache layout"}}
	if err = s.Handoffs.Ingest("transcript", "inode", 50, events); err != nil {
		t.Fatal(err)
	}
	if err = s.Handoffs.Fail(events, errors.New("native provider unavailable")); err != nil {
		t.Fatal(err)
	}
	if batch, _ := s.Handoffs.Batch(); len(batch) != 0 {
		t.Fatal("failed extraction spun unchanged work")
	}
	issues, _ := s.Handoffs.Issues("work")
	if len(issues) != 1 {
		t.Fatalf("coverage failure missing: %+v", issues)
	}
	if err = s.Handoffs.Resume(); err != nil {
		t.Fatal(err)
	}
	claims := []handoff.Claim{{ID: "claim", Event: events[0], Kind: "review", Target: "#42", Question: "Review #42", Quote: "Review #42", Topic: "review-42"}}
	if err = s.Handoffs.Classified(events, claims); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = core.OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	batch, _ := s.Handoffs.Batch()
	pending, _ := s.Handoffs.Claims("work", true)
	if len(batch) != 0 || len(pending) != 1 || pending[0].Event.Text != "Review #42" {
		t.Fatal("model result or evidence lost on restart")
	}
	if err = s.Handoffs.Classified(events, claims); err != nil {
		t.Fatal(err)
	}
	pending, _ = s.Handoffs.Claims("work", true)
	if len(pending) != 1 {
		t.Fatal("model replay duplicated an obligation")
	}
	if err = s.Handoffs.Applied("claim", "review:https://github.com/example/query/pull/42"); err != nil {
		t.Fatal(err)
	}
	target, err := s.Handoffs.Target("claim")
	if err != nil || target != "review:https://github.com/example/query/pull/42" {
		t.Fatal("applied identity lost")
	}
	if pending, _ = s.Handoffs.Claims("work", true); len(pending) != 0 {
		t.Fatal("applied claim remained pending")
	}
}
