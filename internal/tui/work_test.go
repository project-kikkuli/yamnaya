package tui

import (
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/work"
)

func continuing(t *testing.T) (Model, work.Scope) {
	t.Helper()
	m, c, _ := setup(t)
	if err := c.ObserveWork(m.board); err != nil {
		t.Fatal(err)
	}
	all, err := c.Store.Work.List()
	if err != nil || len(all) == 0 {
		t.Fatalf("no continuing work: %v", err)
	}
	w := all[0]
	for _, x := range all {
		if x.Manager == "lead" {
			w = x
		}
	}
	w, err = c.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "brief", Actor: w.Manager, Brief: work.Brief{Assessment: "Rollback is proven; the customer promise needs a judgment", Rationale: "Retain a tested recovery path", Uncertainty: "First-cohort traffic remains unproven", Next: "Continue SDK compatibility checks", Context: 0}})
	if err != nil {
		t.Fatal(err)
	}
	return refresh(t, m), w
}

func TestTerminalReviewLoopRetainsContextAndPreservesCursorAcrossRefresh(t *testing.T) {
	m, w := continuing(t)
	var err error
	apply := func(e work.Event) {
		t.Helper()
		e.Work, e.Expected = w.ID, w.Revision
		w, err = m.core.Store.Work.Commit(e)
		if err != nil {
			t.Fatal(err)
		}
	}
	apply(work.Event{Type: "observe", Review: work.Review{ID: "doc", URL: "https://example.test/design", Kind: "document", Title: "Permission inheritance", Version: "v3", Order: 3, Requester: "Mira"}})
	apply(work.Event{Type: "prepare", Target: "doc", Review: work.Review{Version: "v3", Question: "Accept the design?", Recommendation: "Add the nested example", Evidence: "Resolver compared", Changed: "API and console share a rule"}})
	m = refresh(t, m)
	m = press(t, m, "R", "enter")
	if m.mode != modeReview || !strings.Contains(m.View(), "Accept the design?") {
		t.Fatal("review packet did not restore the question")
	}
	m = press(t, m, "f", "Add the nested example", "enter")
	latest, _ := m.core.Store.Work.Get(w.ID)
	if latest.Reviews["doc"].State != work.WithAuthor {
		t.Fatalf("feedback lost obligation: %+v", latest)
	}
	w = latest
	apply(work.Event{Type: "observe", Review: work.Review{ID: "doc", URL: "https://example.test/design", Kind: "document", Title: "Permission inheritance", Version: "v4", Order: 4, Requester: "Mira"}})
	apply(work.Event{Type: "prepare", Target: "doc", Review: work.Review{Version: "v4", Question: "Accept the revised design?", Recommendation: "accept", Evidence: "Nested example agrees", Changed: "Your requested example is added"}})
	m = refresh(t, m)
	if !strings.Contains(m.View(), "Add the nested example") || !strings.Contains(m.View(), "Your requested example is added") {
		t.Fatal("revision lost prior feedback or changed evidence")
	}
	m = press(t, m, "a", "Accept the shared rule", "enter")
	latest, _ = m.core.Store.Work.Get(w.ID)
	if latest.Reviews["doc"].State != work.Completed {
		t.Fatal("terminal judgment was not recorded")
	}
	m = press(t, m, "B")
	if m.mode != modeWork || !strings.Contains(m.View(), "Rollback is proven") {
		t.Fatal("manager assessment is not reachable from review")
	}
	m = press(t, m, "tab")
	before, _ := m.selected()
	key := rowKey(before)
	m = refresh(t, m)
	after, _ := m.selected()
	if rowKey(after) != key {
		t.Fatal("refresh moved selection to different work")
	}
}

func TestTerminalViewsFitAtSupportedSizes(t *testing.T) {
	m, w := continuing(t)
	for _, size := range [][2]int{{48, 24}, {80, 30}, {120, 40}, {180, 50}} {
		for _, keys := range [][]string{{"B"}, {"R"}, {"B", "tab"}, {"B", "tab", "tab"}, {"B", "tab", "tab", "tab"}} {
			t.Run(fmt.Sprint(size, keys), func(t *testing.T) {
				view := m
				view.width, view.height = size[0], size[1]
				for i, p := range view.board.Projects {
					if p.Work != nil && p.Work.ID == w.ID {
						view.detail = view.board.Projects[i].Key
						view.workID = w.ID
					}
				}
				view = press(t, view, keys...)
				for _, line := range strings.Split(view.View(), "\n") {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("view exceeds terminal width: %q", ansi.Strip(line))
					}
				}
			})
		}
	}
}

func TestCommandPaletteParsesQuotedDirectionWithoutAShell(t *testing.T) {
	got, err := commandWords(`work purpose abc "Keep $(danger) literal"`)
	if err != nil || len(got) != 4 || got[3] != "Keep $(danger) literal" {
		t.Fatalf("arguments: %q %v", got, err)
	}
	if _, err = commandWords(`work purpose abc "unfinished`); err == nil {
		t.Fatal("unfinished quote accepted")
	}
}

func TestReturningFromAnotherProjectRestoresTheBriefAndItsPlace(t *testing.T) {
	m, w := continuing(t)
	m.workID = w.ID
	m.openWork(w)
	m.workPanel, m.scrollY = 2, 3
	other := work.Scope{ID: "other", Name: "Other project"}
	m.push(modeReview)
	m.workID, m.reviewID = "other", "review"
	m.pop()
	if m.workID != w.ID || m.workPanel != 2 || m.scrollY != 3 || m.workID == other.ID {
		t.Fatal("navigation lost project context")
	}
}

func TestTerminalProgramEditsContextOpensSourceAndMessagesItsManager(t *testing.T) {
	m, w := continuing(t)
	var err error
	w, err = m.core.Store.Work.Commit(work.Event{Work: w.ID, Expected: w.Revision, Type: "observe", Review: work.Review{ID: "doc", URL: "https://example.test/design", Title: "Resolver proposal", Version: "v1", Order: 1}})
	if err != nil {
		t.Fatal(err)
	}
	m = refresh(t, m)
	m.mode, m.workID, m.reviewID = modeReview, w.ID, "doc"
	dir := t.TempDir()
	marker := filepath.Join(dir, "opened")
	t.Setenv("YM_OPENED_SOURCE", marker)
	for _, name := range []string{"open", "xdg-open"} {
		if err = os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$YM_OPENED_SOURCE\"\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	editor := filepath.Join(dir, "edit-context")
	if err = os.WriteFile(editor, []byte("#!/bin/sh\nprintf '%s' 'Preserve both API and SDK' > \"$1\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("EDITOR", editor)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	input, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	p := tea.NewProgram(m, tea.WithInput(input), tea.WithOutput(io.Discard), tea.WithoutRenderer(), tea.WithoutSignalHandler())
	finished := make(chan error, 1)
	go func() { _, err := p.Run(); finished <- err }()
	defer func() {
		p.Quit()
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	key := func(k string) { p.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}) }
	wait := func(test func() bool) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if test() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("terminal action did not complete")
	}
	key("V")
	wait(func() bool { raw, _ := os.ReadFile(marker); return string(raw) == "https://example.test/design" })
	key("C")
	wait(func() bool {
		latest, _ := m.core.Store.Work.Get(w.ID)
		return latest.Context.Text == "Preserve both API and SDK"
	})
	key("B")
	key("s")
	key("Retain the SDK example")
	p.Send(tea.KeyMsg{Type: tea.KeyEnter})
	received := false
	wait(func() bool {
		updates, _ := m.core.Store.Work.Inbox(w.Manager)
		for _, update := range updates {
			if strings.Contains(update, "Retain the SDK example") {
				received = true
			}
		}
		return received
	})
	latest, err := m.core.Store.Work.Get(w.ID)
	if err != nil || latest.Context.Revision != 1 || latest.Bindings[w.Manager] != 0 {
		t.Fatalf("editor delivery was mistaken for incorporation: %+v %v", latest, err)
	}
}
