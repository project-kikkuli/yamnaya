package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func intakeChecklist(t *testing.T) (Model, work.Scope) {
	t.Helper()
	m, c, _ := setup(t)
	w, err := c.Store.Work.Intake()
	if err != nil {
		t.Fatal(err)
	}
	for i, address := range []string{"https://github.com/example/query/pull/1", "https://www.notion.so/11111111222233334444555555555555"} {
		w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "watch", Source: work.Source{Kind: []string{"github", "notion"}[i], URL: address, Requester: "Mira"}})
		if err != nil {
			t.Fatal(err)
		}
		w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "observe", Review: work.Review{ID: address, URL: address, Kind: "request", Title: []string{"Caller migration", "Permission proposal"}[i], Requester: "Mira", Version: "v1", Order: 1}})
		if err != nil {
			t.Fatal(err)
		}
	}
	w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "observe", Review: work.Review{ID: "closed", URL: "https://example.test/closed", Title: "Old closed request", Version: "v1", Order: 1, Closed: true}})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m.cursor = 0
	return m, w
}

func TestIncomingStackOpensSourcesAndDrainsAsAVersionedChecklist(t *testing.T) {
	for _, start := range []string{"enter", "o", "V"} {
		t.Run(start, func(t *testing.T) {
			m, w := intakeChecklist(t)
			dir := t.TempDir()
			marker := filepath.Join(dir, "opened")
			t.Setenv("YM_OPENED_SOURCE", marker)
			for _, name := range []string{"open", "xdg-open"} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$YM_OPENED_SOURCE\"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			if !strings.Contains(m.View(), "reviews (2)") {
				t.Fatalf("stack has no actionable count: %s", m.View())
			}
			m = press(t, m, start)
			if m.mode != modeReviews || len(m.rows()) != 2 || m.reviewWork != w.ID {
				t.Fatalf("stack opened an empty brief or history: mode=%v rows=%+v", m.mode, m.rows())
			}
			first, second := m.rows()[0].review.ID, m.rows()[1].review.ID
			if start == "enter" {
				m = press(t, m, "o")
			}
			raw, _ := os.ReadFile(marker)
			if string(raw) != first {
				t.Fatalf("opened %q instead of the selected request %q", raw, first)
			}
			m = press(t, m, "down", "V")
			raw, _ = os.ReadFile(marker)
			if string(raw) != second {
				t.Fatalf("could not open the next source: %q", raw)
			}
			m = press(t, m, "enter", "p")
			if m.reviewID != first || m.reviewPosition() != "1 of 2" {
				t.Fatal("previous request did not restore its context")
			}
			m = press(t, m, "n")
			if m.reviewID != second || m.reviewPosition() != "2 of 2" {
				t.Fatal("next request did not restore its context")
			}
			m = press(t, m, "B", "esc")
			if m.mode != modeReview || m.reviewID != second || m.reviewPosition() != "2 of 2" {
				t.Fatal("reading the brief lost the checklist position")
			}
			m = press(t, m, "p", "x")
			if m.mode != modeReview || m.reviewID != second || m.reviewPosition() != "1 of 1" {
				t.Fatalf("marking reviewed did not advance: %+v", m)
			}
			m = press(t, m, "x")
			if m.mode != modeReviews || len(m.rows()) != 0 || len(m.managementAttention()) != 0 || !strings.Contains(m.View(), "All caught up") {
				t.Fatalf("empty checklist left an unassigned notification: %s", m.View())
			}
			current, err := m.core.Store.Work.Get(w.ID)
			if err != nil || len(current.Sources) != 2 || len(current.Reviews) != 3 {
				t.Fatal("checklist completion discarded source tracking or review history")
			}
			for _, id := range []string{first, second} {
				r := current.Reviews[id]
				if r.State != work.Reviewed || r.ReviewedVersion != "v1" || r.Judgment != "" || r.Recommendation != "" {
					t.Fatalf("manual review fabricated an approval packet: %+v", r)
				}
			}
			m = press(t, m, "tab", "u", "tab", "tab", "tab")
			if m.reviewFilter != "pending" || len(m.rows()) != 1 {
				t.Fatal("reviewed history cannot undo back into the checklist")
			}
			m = press(t, m, " ")
			if len(m.rows()) != 0 || len(m.managementAttention()) != 0 {
				t.Fatal("Space did not check off an item directly from the list")
			}
			current, _ = m.core.Store.Work.Get(w.ID)
			_, err = m.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: current.Revision, Type: "observe", Review: work.Review{ID: first, URL: first, Title: "Caller migration revised", Version: "v2", Order: 2}})
			if err != nil {
				t.Fatal(err)
			}
			m = refresh(t, m)
			if len(m.rows()) != 1 || m.rows()[0].review.ID != first || len(m.managementAttention()) != 1 {
				t.Fatal("changed source failed to return to the pending checklist")
			}
		})
	}
}

func TestChecklistFailureDoesNotAdvanceOrOverwriteNewerEvidence(t *testing.T) {
	for _, change := range []string{"context", "source", "unavailable"} {
		t.Run(change, func(t *testing.T) {
			m, w := intakeChecklist(t)
			m = press(t, m, "enter", "enter")
			id := m.reviewID
			switch change {
			case "context":
				_, err := m.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "context", Text: "Check both callers"})
				if err != nil {
					t.Fatal(err)
				}
			case "source":
				m.core.CheckReview = func(work.Source) (work.Review, error) {
					return work.Review{ID: id, URL: id, Version: "v2", Order: 2}, nil
				}
			case "unavailable":
				m.core.CheckReview = func(work.Source) (work.Review, error) {
					return work.Review{}, os.ErrPermission
				}
			}
			m = press(t, m, "x")
			current, err := m.core.Store.Work.Get(w.ID)
			if err != nil || m.mode != modeReview || m.reviewID != id || current.Reviews[id].State == work.Reviewed || !strings.Contains(m.message, "error:") {
				t.Fatalf("failed completion advanced or hid changed evidence: %s %+v", m.message, current)
			}
		})
	}
}

func TestRemoteChecklistAdvancesOnlyAfterTheOriginAppliesIt(t *testing.T) {
	m, w := intakeChecklist(t)
	w.Host, w.ID = "builder", "builder/"+w.ID
	m.board = core.Board{Projects: []core.Project{{Key: w.ID, Work: &w}}}
	m = press(t, m, "enter", "enter")
	id := m.reviewID
	next, cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(Model)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.reviewID != id || m.pendingReview == nil || len(m.reviewQueueRows()) != 2 {
		t.Fatal("queued remote review was reported as applied")
	}
	commands, err := m.core.Store.Work.RemoteCommands("builder")
	if err != nil || len(commands) != 1 || commands[0].Type != "reviewed" {
		t.Fatalf("checklist update did not reach remote outbox: %+v %v", commands, err)
	}
	r := w.Reviews[id]
	r.State, r.ReviewedVersion = work.Reviewed, r.Version
	w.Reviews[id] = r
	w.Revision++
	next, _ = m.Update(boardMsg{board: core.Board{Projects: []core.Project{{Key: w.ID, Work: &w}}}})
	m = next.(Model)
	if m.pendingReview != nil || m.reviewID == id || len(m.reviewQueueRows()) != 1 {
		t.Fatal("applied remote receipt did not advance to the next request")
	}
}

func TestDelayedChecklistReceiptDoesNotReplaceNewerBoardEvidence(t *testing.T) {
	m, w := intakeChecklist(t)
	m = press(t, m, "enter", "enter")
	id := m.reviewID
	next, cmd := m.key(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("x")})
	m = next.(Model)
	receipt := cmd().(reviewActionMsg)
	current, err := m.core.Store.Work.Get(w.ID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: current.Revision, Type: "observe", Review: work.Review{ID: id, URL: id, Version: "v2", Order: 2}})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	next, _ = m.Update(receipt)
	m = next.(Model)
	latest, _ := m.workScope()
	if m.reviewID != id || latest.Reviews[id].Version != "v2" || !latest.Reviews[id].Pending() {
		t.Fatal("a delayed review receipt replaced new evidence or advanced past it")
	}
}
