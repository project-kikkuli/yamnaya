package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/x/ansi"
)

var (
	webLink  = regexp.MustCompile(`https?://[^\s\x1b<>"']+[^\s\x1b<>"'.,;:!?)\]]`)
	linkOpen = regexp.MustCompile(`\x1b\]8;[^;\a\x1b]*;([^\a\x1b]*)(?:\a|\x1b\\)`)
)

// linkedWrap wraps text to width, every URL whole on screen and a hyperlink on each line it spans, so a terminal that
// opens links opens the full URL from any piece of it.
func linkedWrap(text string, width int) string {
	text = webLink.ReplaceAllStringFunc(text, func(u string) string { return ansi.SetHyperlink(u) + u + ansi.ResetHyperlink() })
	lines := strings.Split(ansi.Wrap(text, width, " "), "\n")
	open := ""
	for i, line := range lines {
		if open != "" {
			line = ansi.SetHyperlink(open) + line
		}
		for _, m := range linkOpen.FindAllStringSubmatch(line, -1) {
			open = m[1]
		}
		if open != "" {
			line += ansi.ResetHyperlink()
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}
