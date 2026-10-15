package codex

import (
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/screen"
)

const footerLines = 6

// ParsePrompt reads a Codex approval, folder-trust or open-in-another-app dialog off the screen.
func (a *Adapter) ParsePrompt(text string) (core.Prompt, bool) { return ParsePrompt(text) }

func ParsePrompt(text string) (core.Prompt, bool) {
	if OpenElsewhere(text) {
		return core.Prompt{Tool: "open in another app"}, true
	}
	lines := screen.Lines(text)
	if !screen.FooterNear(lines, footerLines, "Press enter to confirm or esc to cancel", "enter continue · esc") {
		return core.Prompt{}, false
	}
	switch {
	case screen.Has(lines, "Trust this folder?") && screen.Has(lines, "Trust and continue"):
		return core.Prompt{Tool: "trust folder", Detail: screen.After(lines, "Folder access")}, true
	case screen.Has(lines, "Would you like to run the following command?"):
		return core.Prompt{Tool: "Shell", Detail: screen.LastPrefixed(lines, "$ ")}, true
	case screen.Has(lines, "Would you like to make the following edits?"):
		return core.Prompt{Tool: "Edit", Detail: screen.LastPrefixed(lines, "Destination: ")}, true
	}
	return core.Prompt{}, false
}

// OpenElsewhere reports Codex's notice that another client still holds the conversation, which r retries.
// The shared app-server keeps a quit client's claim for a moment, so a resume started right after the quit can meet it.
func OpenElsewhere(text string) bool {
	lines := screen.Lines(text)
	return screen.Has(lines, "This conversation is open in another app") && screen.FooterNear(lines, footerLines, "r retry")
}

// Composer reads Codex's input box: the › line and its wrapped lines up to the blank line above the footer.
func (a *Adapter) Composer(text string) (string, bool) { return Composer(text) }

func Composer(text string) (string, bool) {
	raw := strings.Split(strings.TrimRight(text, "\n "), "\n")
	start := -1
	for i := len(raw) - 1; i >= 0; i-- {
		if strings.HasPrefix(raw[i], "› ") {
			start = i
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := start + 1
	for end < len(raw) && strings.TrimSpace(raw[end]) != "" && strings.HasPrefix(raw[end], "  ") {
		end++
	}
	footer := screen.Lines(strings.Join(raw[end:], "\n"))
	if len(footer) > 3 || (end < len(raw) && strings.TrimSpace(raw[end]) != "") || screen.FooterNear(footer, 3, "esc to cancel", "enter continue") {
		return "", false
	}
	box := make([]string, 0, end-start)
	for _, l := range raw[start:end] {
		box = append(box, strings.TrimSpace(l))
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.Join(box, " "), "›")), true
}
