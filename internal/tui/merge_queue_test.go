package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func TestMergeTaskAppearsOnTheBoardAndOpensWithoutChecklistCompletion(t *testing.T) {
	m, w := continuing(t)
	url := "https://github.com/example/query/pull/42"
	id := work.MergeReviewID(url)
	var err error
	w, err = m.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "merge-status", Actor: "GitHub", Review: work.Review{ID: id, URL: url, Action: "merge", Kind: "merge", Title: "Caller migration", State: work.Ready, Version: "head1:ready", Question: "Merge this pull request?", Recommendation: "Merge this PR on GitHub", Evidence: "12 checks passing; approved; clean merge state; no open threads"}})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	target := -1
	for i, r := range m.rows() {
		if r.review != nil && r.review.ID == id {
			target = i
			break
		}
	}
	if target < 0 {
		t.Fatalf("merge task is absent: %s", m.View())
	}
	for m.cursor < target {
		m = press(t, m, "down")
	}
	for m.cursor > target {
		m = press(t, m, "up")
	}
	if r, ok := m.selected(); !ok || r.review == nil || r.review.Action != "merge" || !strings.Contains(m.View(), "Merge this pull request?") {
		t.Fatalf("merge task did not appear in Needs You: %s", m.View())
	}
	dir := t.TempDir()
	marker := filepath.Join(dir, "opened")
	t.Setenv("YM_OPENED_SOURCE", marker)
	for _, name := range []string{"open", "xdg-open"} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$YM_OPENED_SOURCE\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	m = press(t, m, "o", "enter", "a")
	raw, _ := os.ReadFile(marker)
	latest, _ := m.core.Store.Work.Get(w.ID)
	if string(raw) != url || m.mode != modeReview || latest.Reviews[id].State != work.Ready || latest.Reviews[id].Judgment != "" {
		t.Fatal("source opening failed or a local key pretended to merge the PR")
	}
	m = press(t, m, "x")
	latest, _ = m.core.Store.Work.Get(w.ID)
	if !latest.Reviews[id].Dismissed || latest.Reviews[id].Pending() {
		t.Fatal("x did not dismiss the merge item")
	}
	if m.mode != modeReviews {
		t.Fatal("dismissal did not finish the checklist")
	}
	for i := 0; i < 10 && m.reviewFilter != "dismissed"; i++ {
		m = press(t, m, "tab")
	}
	if m.reviewFilter != "dismissed" || len(m.rows()) != 1 {
		t.Fatalf("dismissed queue unavailable: %s", m.View())
	}
	m = press(t, m, "u")
	latest, _ = m.core.Store.Work.Get(w.ID)
	if latest.Reviews[id].Dismissed || !latest.Reviews[id].Pending() {
		t.Fatal("u did not restore the merge item")
	}
	latest, err = m.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: latest.Revision, Type: "merge-status", Actor: "GitHub", Review: work.Review{ID: id, URL: url, Action: "merge", Kind: "merge", Title: "Caller migration", State: work.Completed, Closed: true, Version: "head1:merged"}})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m = press(t, m, "esc")
	for _, r := range m.rows() {
		if r.review != nil && r.review.ID == id {
			t.Fatal("merged PR remained in Needs You")
		}
	}
}

func TestLandArchivesMissionWithoutLaunchRecord(t *testing.T) {
	m, w := continuing(t)
	m.core.Procs = func() ([]proc.Proc, error) { return nil, nil }
	m.core.Harnesses = nil
	m = refresh(t, m)
	m = to(t, m, func(r row) bool {
		return r.project != nil && r.project.Work != nil && r.project.Work.ID == w.ID && r.session == nil && r.review == nil && r.attention == ""
	})
	m = press(t, m, "L")
	if m.mode != modeConfirm || !strings.Contains(m.View(), "archive ") {
		t.Fatalf("L silently failed: %s", m.View())
	}
	m = press(t, m, "y")
	latest, err := m.core.Store.Work.Get(w.ID)
	if err != nil || latest.State != "archived" {
		t.Fatalf("archive failed: %+v %v", latest, err)
	}
	for _, p := range m.board.Projects {
		if p.Work != nil && p.Work.ID == w.ID && p.Shown() {
			t.Fatal("archived mission remains visible")
		}
	}
}
