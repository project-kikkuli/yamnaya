package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func searchHelp(t *testing.T, width int, query string) string {
	t.Helper()
	m := New(&core.Core{})
	m.width, m.height = width, 200
	m = press(t, m, "?", "/")
	for _, r := range query {
		m = press(t, m, string(r))
	}
	m = press(t, m, "enter")
	return ansi.Strip(m.View().Content)
}

func TestHelpSearchFilters(t *testing.T) {
	for _, c := range []struct {
		name, query string
		width       int
		has, lacks  []string
	}{
		{"key", "K", 120, []string{"kill, after one y/n", "Act on the selected row"}, []string{"this page", "hide or show the scene"}},
		{"revive word", "revive", 120, []string{"enter revives one with its worktree", "an exited launch: revive it"}, []string{"Percentages", "hide or show the scene"}},
		{"case insensitive", "THEME", 120, []string{"switch the theme of that scene"}, []string{"kill, after one y/n"}},
		{"merge", "merge", 120, []string{"merge a mission into another", "Act on the selected row", "More", "Percentages"}, []string{"back or quit", "hide or show the scene"}},
		{"percent section", "gates", 120, []string{"Percentages", "The 6 gates weigh equally"}, []string{"Move"}},
		{"legend meaning", "waiting", 120, []string{"an agent stopped and waiting on you", "Theme: "}, []string{"kill, after one y/n"}},
		{"no match", "zzzqq", 120, []string{"no help matches 'zzzqq'"}, []string{"Move", "Percentages"}},
		{"wrapped entry by its tail", "worktree ym made", 80, []string{"K", "kill, after one y/n", "drop the row", "Teardown runs in the background"}, []string{"Percentages"}},
		{"wrapped entry shown whole", "needs ym running inside tmux", 80, []string{"open the selected row's session in a new tmux pane", "needs ym running inside tmux"}, []string{"kill, after one y/n"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := searchHelp(t, c.width, c.query)
			flat := strings.Join(strings.Fields(got), " ")
			for _, w := range c.has {
				if !strings.Contains(flat, w) {
					t.Errorf("missing %q:\n%s", w, got)
				}
			}
			for _, w := range c.lacks {
				if strings.Contains(flat, w) {
					t.Errorf("should not show %q:\n%s", w, got)
				}
			}
			for i, l := range strings.Split(got, "\n") {
				if ansi.StringWidth(l) > c.width {
					t.Errorf("line %d is %d wide", i, ansi.StringWidth(l))
				}
			}
		})
	}
}

func TestHelpSearchKeys(t *testing.T) {
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	m := New(&core.Core{Store: st})
	m.loaded, m.peaks.hidden = true, true
	m.width, m.height = 80, 24
	m = press(t, m, "?", "/", "m", "e", "r", "g", "e")
	if !m.helpTyping || !strings.Contains(ansi.Strip(m.View().Content), "/merge") {
		t.Fatal("the search prompt is not shown while typing")
	}
	m = press(t, m, "backspace", "e")
	if m.helpQ != "merge" {
		t.Fatalf("query %q", m.helpQ)
	}
	m = press(t, m, "enter")
	if m.helpTyping || m.helpQ != "merge" || m.mode != modeHelp {
		t.Fatal("enter should keep the filter and return to scrolling")
	}
	m = press(t, m, "n")
	if m.helpAt != 1 {
		t.Fatalf("n went to match %d", m.helpAt)
	}
	m = press(t, m, "N")
	if m.helpAt < 1 {
		t.Fatal("N lost the match")
	}
	m = switchTheme(t, m)
	if m.helpQ != "merge" || m.mode != modeHelp {
		t.Fatal("L should still switch the theme inside help")
	}
	m = press(t, m, "esc")
	if m.helpQ != "" || m.mode != modeHelp {
		t.Fatal("first esc should clear the filter and stay in help")
	}
	m = press(t, m, "esc")
	if m.mode == modeHelp {
		t.Fatal("second esc should leave help")
	}
	m = press(t, m, "?", "/", "x", "esc")
	if m.helpTyping || m.helpQ != "" || m.mode != modeHelp {
		t.Fatal("esc while typing should clear the search")
	}
}
