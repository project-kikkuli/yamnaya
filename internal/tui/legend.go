package tui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/scene"
)

func helpText(width int, s scene.Scene) string {
	g := func(s string) string {
		return "   " + strings.ReplaceAll(ansi.Wrap(s, max(20, width-5), " "), "\n", "\n     ")
	}
	paint := func(e scene.Entry) string {
		if e.Color == "" || !pal.color {
			return e.Glyph
		}
		return lipgloss.NewStyle().Foreground(lipgloss.Color(e.Color)).Render(e.Glyph)
	}
	t := scene.Lookup(s.Theme)
	legend := t.Legend(s)
	var names []string
	for _, o := range scene.Themes() {
		names = append(names, o.Name())
	}
	var b strings.Builder
	b.WriteString(reflow(Help, width))
	para := func(s string) { b.WriteString(reflow(" "+s, width)) }
	b.WriteString("\n")
	para("Theme: " + t.Name() + " (L switches to the next of " + strings.Join(names, ", ") + "; A hides it)")
	for _, e := range legend.Entries {
		if e.Signal.IsWeather() {
			continue
		}
		line := e.Signal.When() + ": " + e.Look
		if e.Glyph != "" {
			line = paint(e) + " " + line
		}
		b.WriteString(g(line) + "\n")
	}
	b.WriteString("\n")
	para("Weather (per unit; a group takes the worst of its members: Eruption beats Storm beats Summit beats Clouds)")
	for _, e := range legend.Entries {
		if !e.Signal.IsWeather() {
			continue
		}
		look := e.Look
		if e.Glyph != "" {
			look += " " + paint(e)
		}
		b.WriteString(g(e.Signal.String()+" ("+look+"): "+e.Signal.When()) + "\n")
	}
	b.WriteString("\n")
	para("Decoration only, no meaning: " + strings.Join(legend.Decoration, ", ") + ".")
	para("Colors never mark a goal or repository: only weather, pull request state and time of day.")
	b.WriteString("\n")
	para("Percentages")
	for _, l := range []string{
		"done % = (finished crew + gates passed) / (crew + 6 x pull requests and pushed branches not closed)",
		"Crew is every agent beyond the main session; finished = no longer busy or waiting. The 6 gates weigh equally.",
		"A verifying goal shows at least 90%; a done goal 100%; a merged group averages its members.",
	} {
		b.WriteString(g(l) + "\n")
	}
	return b.String()
}

const keyColumn = 16

// reflow wraps the help prose and key list to width: paragraphs hang at one space, key entries under their text.
func reflow(text string, width int) string {
	var out []string
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		for _, l := range strings.Split(ansi.Wrap(strings.Join(para, " "), max(20, width-2), " "), "\n") {
			out = append(out, " "+l)
		}
		para = nil
	}
	entry := func(key string, body []string) {
		if len(body) == 0 {
			out = append(out, "   "+key)
			return
		}
		lines := strings.Split(ansi.Wrap(strings.Join(body, " "), max(20, width-keyColumn-1), " "), "\n")
		first := "   " + key
		if ansi.StringWidth(first) >= keyColumn {
			out = append(out, first)
			first = ""
		}
		for i, l := range lines {
			head := strings.Repeat(" ", keyColumn)
			if i == 0 && first != "" {
				head = first + strings.Repeat(" ", keyColumn-ansi.StringWidth(first))
			}
			out = append(out, head+l)
		}
	}
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := lines[i]
		indent := len(l) - len(strings.TrimLeft(l, " "))
		switch {
		case strings.TrimSpace(l) == "":
			flush()
			out = append(out, "")
		case indent == 1:
			if len(para) > 0 && strings.HasSuffix(para[len(para)-1], ".") {
				flush()
			}
			para = append(para, strings.TrimSpace(l))
		case indent == 3:
			flush()
			body := strings.TrimSpace(l)
			key, rest, gap := strings.Cut(body, "  ")
			var parts []string
			if gap {
				parts = append(parts, strings.TrimSpace(rest))
			}
			j := i + 1
			for ; j < len(lines) && len(lines[j])-len(strings.TrimLeft(lines[j], " ")) >= keyColumn; j++ {
				parts = append(parts, strings.TrimSpace(lines[j]))
			}
			if !gap && len(parts) == 0 {
				out = append(out, l)
				break
			}
			entry(key, parts)
			i = j - 1
		default:
			flush()
			out = append(out, l)
		}
	}
	flush()
	return strings.Join(out, "\n") + "\n"
}
