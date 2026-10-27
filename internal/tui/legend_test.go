package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestHelpLegendExplainsEveryWeatherAndGlyph(t *testing.T) {
	legend := ansi.Strip(helpText(200))
	for _, want := range []string{
		"Clear:", "Clouds:", "Storm:", "Eruption:", "Summit:", "Sky tint",
		"λ agent at work", "λ agent stopped", "▴ N resting", "▲ pull request camp", "▲ camp whose CI passed", "▲ camp whose CI fails",
		"⚑ merged pull request", "⚑ on a summit", "✸ red flare", "╱", "Decoration only", "done %", "90%", "100%",
	} {
		if !strings.Contains(legend, want) {
			t.Errorf("help does not explain %q", want)
		}
	}
}

func TestHelpScrollsWhenItDoesNotFit(t *testing.T) {
	m := New(&core.Core{})
	m.width, m.height = 80, 24
	m = press(t, m, "?")
	first := ansi.Strip(m.View().Content)
	if strings.Contains(first, "Percentages") {
		t.Fatalf("the end of help is on the first screen:\n%s", first)
	}
	for range 40 {
		m = press(t, m, "pgdown")
	}
	if last := ansi.Strip(m.View().Content); !strings.Contains(last, "a done goal 100%") {
		t.Fatalf("help does not scroll to its end:\n%s", last)
	}
	m = press(t, m, "esc")
	if m.mode == modeHelp {
		t.Fatal("esc should leave help")
	}
}
