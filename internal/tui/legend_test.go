package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func TestHelpExplainsEverySignalOfTheThemeDrawn(t *testing.T) {
	for _, th := range scene.Themes() {
		t.Run(th.Name(), func(t *testing.T) {
			s := scene.Scene{Theme: th.Name(), Dark: true}
			help := strings.Join(strings.Fields(ansi.Strip(helpText(200, s))), " ")
			legend := th.Legend(s)
			for _, e := range legend.Entries {
				for _, want := range []string{e.Signal.When(), e.Look, e.Glyph} {
					if !strings.Contains(help, strings.Join(strings.Fields(want), " ")) {
						t.Errorf("help for %s does not say %q", e.Signal, want)
					}
				}
			}
			for _, d := range legend.Decoration {
				if !strings.Contains(help, d) {
					t.Errorf("help does not call %q decoration", d)
				}
			}
			if !strings.Contains(help, "Theme: "+th.Name()) {
				t.Errorf("help does not name the theme %s", th.Name())
			}
		})
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

func TestHelpFitsEveryWidthAndKeepsEveryKey(t *testing.T) {
	for _, w := range []int{60, 80, 120} {
		text := ansi.Strip(helpText(w, scene.Scene{}))
		for i, l := range strings.Split(text, "\n") {
			if ansi.StringWidth(l) > w {
				t.Errorf("width %d line %d is %d wide: %q", w, i, ansi.StringWidth(l), l)
			}
		}
		for _, want := range []string{"kill, after one y/n", "page down or up in an agent's page or a text page", "top or bottom of the list", "NO_COLOR turns color off", "Eruption (", "A verifying goal"} {
			if !strings.Contains(strings.Join(strings.Fields(text), " "), want) {
				t.Errorf("width %d lost %q", w, want)
			}
		}
	}
}
