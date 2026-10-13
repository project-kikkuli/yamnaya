package codex

import (
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/screen"
)

const footerLines = 6

// ParsePrompt reads a Codex approval or folder-trust dialog off the screen.
func (a *Adapter) ParsePrompt(text string) (core.Prompt, bool) { return ParsePrompt(text) }

func ParsePrompt(text string) (core.Prompt, bool) {
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
