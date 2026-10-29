package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/project-kikkuli/yamnaya/internal/clipboard"
	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestYPutsCleanTextOfABriefAContextAndAnItemOnTheClipboard(t *testing.T) {
	was := clipWay
	clipWay = func() clipboard.Way { return clipboard.OSC52 }
	t.Cleanup(func() { clipWay = was })
	ask := "fix \"copy & paste\" in the owner's briefs\n\n## Steering\n\nkeep <pre> blocks & it's done"
	nested := "fix \"copy & paste\" in the owner's briefs\n\n### Steering\n\nkeep <pre> blocks & it's done"
	assessment := `the "page" & its <pre> block don't wrap`
	for _, tc := range []struct {
		name string
		item bool
		keys []string
		want []string
	}{
		{"brief from the menu", false, []string{"y", "b"}, []string{"## Assessment\n\n" + assessment + "\n", "## Ask\n\n" + nested + "\n"}},
		{"brief from its page", false, []string{"b", "y"}, []string{"## Assessment\n\n" + assessment + "\n", "## Ask\n\n" + nested + "\n"}},
		{"context", false, []string{"y", "c"}, []string{ask + "\n"}},
		{"item", true, []string{"y"}, []string{"Ship it?\n", "rec: yes\n", "option: a = does a\n"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, ms := goalSetup(t, ask)
			b := core.Brief{Assessment: assessment, Changed: "it's fixed & pushed", Uncertainty: "none", Next: "1) b\n2) y", WaitingOn: "none"}
			if _, err := c.WriteBrief(ms[0].Session, b); err != nil {
				t.Fatal(err)
			}
			at := isNode(ms[0].ID)
			if tc.item {
				at = func(r row) bool { return r.item != nil && r.item.Question == "Ship it?" }
			}
			m = press(t, to(t, refresh(t, m), at), tc.keys[:len(tc.keys)-1]...)
			_, cmd := m.Update(keyOf(tc.keys[len(tc.keys)-1]))
			if cmd == nil {
				t.Fatalf("%v copied nothing", tc.keys)
			}
			batch, ok := cmd().(tea.BatchMsg)
			if !ok || len(batch) == 0 {
				t.Fatalf("%v did not write the clipboard through the terminal", tc.keys)
			}
			text := fmt.Sprint(batch[0]())
			for _, w := range tc.want {
				if !strings.Contains(text, w) {
					t.Fatalf("copied text lacks %q:\n%s", w, text)
				}
			}
			for _, bad := range []string{"&#", "&amp;", "&quot;", "\x1b", "\r", "\n\n\n"} {
				if strings.Contains(text, bad) {
					t.Fatalf("copied text carries %q:\n%q", bad, text)
				}
			}
			for i, line := range strings.Split(text, "\n") {
				if strings.TrimRight(line, " \t") != line {
					t.Fatalf("line %d has trailing whitespace: %q", i+1, line)
				}
			}
			if !strings.HasSuffix(text, "\n") || strings.HasSuffix(text, "\n\n") {
				t.Fatalf("copied text does not end in one newline: %q", text)
			}
		})
	}
}

func TestATextPageLeavesTheMouseToTheTerminalSoDraggingSelects(t *testing.T) {
	m, _, ms := goalSetup(t, "make the importer handle CSV")
	m = to(t, refresh(t, m), isNode(ms[0].ID))
	for _, tc := range []struct {
		keys []string
		want tea.MouseMode
	}{
		{nil, tea.MouseModeCellMotion},
		{[]string{"b"}, tea.MouseModeNone},
		{[]string{"b", "esc"}, tea.MouseModeCellMotion},
		{[]string{"?"}, tea.MouseModeNone},
		{[]string{"?", "esc"}, tea.MouseModeCellMotion},
	} {
		if got := press(t, m, tc.keys...).View().MouseMode; got != tc.want {
			t.Fatalf("after %v mouse mode %v, want %v", tc.keys, got, tc.want)
		}
	}
}
