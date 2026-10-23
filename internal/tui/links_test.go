package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestLinkedWrapKeepsEveryURLWholeAndLinkedOnEachLine(t *testing.T) {
	u := "https://claude.ai/artifact/abcdefghijklmnopqrstuvwxyz0123456789"
	for _, tc := range []struct {
		name, text string
		width      int
	}{
		{"a URL longer than the screen", "Catch-up artifact: " + u + ".", 30},
		{"a URL that fits", "see " + u + " now", 120},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := linkedWrap(tc.text, tc.width)
			if plain := strings.ReplaceAll(ansi.Strip(got), "\n", ""); !strings.Contains(plain, u) {
				t.Errorf("wrapped text %q lost part of %s", plain, u)
			}
			for _, line := range strings.Split(got, "\n") {
				if ansi.StringWidth(line) > tc.width {
					t.Errorf("line %q is wider than %d", ansi.Strip(line), tc.width)
				}
				if strings.Contains(ansi.Strip(line), "claude.ai") || strings.Contains(ansi.Strip(line), "0123") {
					if at := strings.Index(line, ansi.SetHyperlink(u)); at < 0 || strings.LastIndex(line, ansi.ResetHyperlink()) < at {
						t.Errorf("line %q does not link %s on its own", line, u)
					}
				}
			}
			if strings.Contains(got, ansi.SetHyperlink(u+".")) {
				t.Error("the sentence's full stop became part of the link")
			}
		})
	}
}
