package signals

import (
	"strings"
)

// Permission is a harness dialog waiting for the user to allow a tool call or trust a folder.
type Permission struct {
	Tool   string
	Detail string
	// From names the subagent that asked, when it was not the main agent.
	From string
}

func (p Permission) String() string {
	s := p.Tool
	if p.Detail != "" {
		s += ": " + p.Detail
	}
	if p.From != "" {
		s += " · " + p.From
	}
	return s
}

// footerLines is how far from the bottom of the screen a dialog's key hint may sit and still be the live dialog.
const footerLines = 6

var footers = []string{
	"Esc to cancel",
	"Press enter to confirm or esc to cancel",
	"enter continue · esc",
}

var claudeTools = map[string]string{"Bash command": "Bash", "Create file": "Write"}

// ParsePermission reads a pane's visible text for a Claude Code or Codex permission dialog.
func ParsePermission(screen string) (Permission, bool) {
	lines := strings.Split(screen, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	lines = nonBlank(lines)
	if !footerNearBottom(lines) {
		return Permission{}, false
	}
	switch {
	case has(lines, "Is this a project you created or one you trust?") && has(lines, "Yes, I trust this folder"):
		return Permission{Tool: "trust folder", Detail: after(lines, "Accessing workspace:")}, true
	case has(lines, "Trust this folder?") && has(lines, "Trust and continue"):
		return Permission{Tool: "trust folder", Detail: after(lines, "Folder access")}, true
	case has(lines, "Would you like to run the following command?"):
		return Permission{Tool: "Shell", Detail: strings.TrimPrefix(prefixed(lines, "$ "), "$ ")}, true
	case has(lines, "Would you like to make the following edits?"):
		return Permission{Tool: "Edit", Detail: strings.TrimPrefix(prefixed(lines, "Destination: "), "Destination: ")}, true
	}
	ask := lastIndex(lines, func(l string) bool { return strings.HasPrefix(l, "Do you want to ") && strings.HasSuffix(l, "?") })
	if ask < 0 || ask+1 >= len(lines) || !strings.HasPrefix(lines[ask+1], "❯ 1. Yes") {
		return Permission{}, false
	}
	rule := lastIndex(lines[:ask], func(l string) bool { return isRule(l, '─') })
	if rule < 0 || rule+1 >= ask {
		return Permission{Tool: strings.TrimSuffix(strings.TrimPrefix(lines[ask], "Do you want to "), "?")}, true
	}
	title, from, _ := strings.Cut(lines[rule+1], " · from the ")
	p := Permission{Tool: title, From: from}
	if tool, ok := claudeTools[title]; ok {
		p.Tool = tool
	}
	body := lines[rule+2 : ask]
	if title == "Bash command" {
		if dashed := indexOf(body, func(l string) bool { return isRule(l, '╌') }); dashed >= 0 {
			body = body[dashed:]
		}
	}
	for _, l := range body {
		if !isRule(l, '╌') {
			p.Detail = l
			break
		}
	}
	return p, true
}

func nonBlank(lines []string) []string {
	var out []string
	for _, l := range lines {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func footerNearBottom(lines []string) bool {
	start := max(0, len(lines)-footerLines)
	for _, l := range lines[start:] {
		for _, f := range footers {
			if strings.Contains(l, f) {
				return true
			}
		}
	}
	return false
}

func has(lines []string, s string) bool {
	return indexOf(lines, func(l string) bool { return strings.Contains(l, s) }) >= 0
}

func after(lines []string, s string) string {
	i := indexOf(lines, func(l string) bool { return l == s })
	if i < 0 || i+1 >= len(lines) {
		return ""
	}
	return lines[i+1]
}

func prefixed(lines []string, prefix string) string {
	i := lastIndex(lines, func(l string) bool { return strings.HasPrefix(l, prefix) })
	if i < 0 {
		return ""
	}
	return lines[i]
}

func isRule(l string, r rune) bool {
	return len([]rune(l)) >= 10 && strings.Trim(l, string(r)) == ""
}

func indexOf(lines []string, match func(string) bool) int {
	for i, l := range lines {
		if match(l) {
			return i
		}
	}
	return -1
}

func lastIndex(lines []string, match func(string) bool) int {
	for i := len(lines) - 1; i >= 0; i-- {
		if match(lines[i]) {
			return i
		}
	}
	return -1
}
