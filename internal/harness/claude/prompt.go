package claude

import (
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/screen"
)

// footerLines is how far from the bottom a dialog's key hint may sit and still be the live dialog.
const footerLines = 6

var tools = map[string]string{"Bash command": "Bash", "Create file": "Write"}

// ParsePrompt reads a Claude Code permission or folder-trust dialog off the screen.
func (a *Adapter) ParsePrompt(text string) (core.Prompt, bool) { return ParsePrompt(text) }

func ParsePrompt(text string) (core.Prompt, bool) {
	lines := screen.Lines(text)
	if !screen.FooterNear(lines, footerLines, "Esc to cancel", "Press enter to confirm or esc to cancel") {
		return core.Prompt{}, false
	}
	if screen.Has(lines, "Is this a project you created or one you trust?") && screen.Has(lines, "Yes, I trust this folder") {
		return core.Prompt{Tool: "trust folder", Detail: screen.After(lines, "Accessing workspace:")}, true
	}
	ask := screen.LastIndex(lines, func(l string) bool { return strings.HasPrefix(l, "Do you want to ") && strings.HasSuffix(l, "?") })
	if ask < 0 || ask+1 >= len(lines) || !strings.HasPrefix(lines[ask+1], "❯ 1. Yes") {
		return core.Prompt{}, false
	}
	rule := screen.LastIndex(lines[:ask], func(l string) bool { return screen.IsRule(l, '─') })
	if rule < 0 || rule+1 >= ask {
		return core.Prompt{Tool: strings.TrimSuffix(strings.TrimPrefix(lines[ask], "Do you want to "), "?")}, true
	}
	title, from, _ := strings.Cut(lines[rule+1], " · from the ")
	p := core.Prompt{Tool: title, From: from}
	if tool, ok := tools[title]; ok {
		p.Tool = tool
	}
	body := lines[rule+2 : ask]
	if title == "Bash command" {
		if dashed := screen.Index(body, func(l string) bool { return screen.IsRule(l, '╌') }); dashed >= 0 {
			body = body[dashed:]
		}
	}
	for _, l := range body {
		if !screen.IsRule(l, '╌') {
			p.Detail = l
			break
		}
	}
	return p, true
}
