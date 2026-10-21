package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	m = press(t, m, "o", "enter", "x", "a")
	raw, _ := os.ReadFile(marker)
	latest, _ := m.core.Store.Work.Get(w.ID)
	if string(raw) != url || m.mode != modeReview || latest.Reviews[id].State != work.Ready || latest.Reviews[id].Judgment != "" {
		t.Fatal("source opening failed or a local key pretended to merge the PR")
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
