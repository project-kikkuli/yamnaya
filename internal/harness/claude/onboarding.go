package claude

import (
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/harness/screen"
)

// onboardingSteps are the screens Claude Code draws before its prompt. A step shows when lines appear in order as whole lines,
// wrapped or not, and the screen's last line starts with last, so text quoted in a conversation or dialog does not read as one.
var onboardingSteps = []struct {
	step  string
	lines []string
	last  string
}{
	{"theme", []string{"Let's get started.", "Choose the text style that looks best with your terminal"}, ""},
	{"api key", []string{"Detected a custom API key in your environment", "Do you want to use this API key?"}, "Enter to confirm"},
	{"login", []string{"Select login method:"}, ""},
	{"login", nil, "Paste code here if prompted >"},
	{"login", nil, "Login successful. Press"},
	{"security notes", []string{"Security notes:"}, "Press Enter to continue"},
	{"terminal setup", []string{"Use Claude Code's terminal setup?"}, "Enter to confirm"},
	{"chrome", []string{"Claude in Chrome"}, "Enter to confirm"},
}

// Onboarding reports the first-run onboarding step the screen shows, which hides the input box until the owner finishes it.
func (a *Adapter) Onboarding(text string) (string, bool) { return ParseOnboarding(text) }

func ParseOnboarding(text string) (string, bool) {
	if _, ok := Composer(text); ok {
		return "", false
	}
	lines := screen.Lines(text)
	if len(lines) == 0 || screen.Index(lines, conversationLine) >= 0 {
		return "", false
	}
	for _, s := range onboardingSteps {
		if strings.HasPrefix(lines[len(lines)-1], s.last) && inOrder(lines, s.lines) {
			return s.step, true
		}
	}
	return "", false
}

// conversationLine is a line only Claude Code's conversation view draws: tool output or the header above it.
func conversationLine(l string) bool {
	return strings.HasPrefix(l, "⎿") || strings.HasPrefix(l, "▐▛")
}

// inOrder reports whether each wanted line appears after the one before, whole, across as many screen lines as it wraps to.
func inOrder(lines, wanted []string) bool {
	from := 0
	for _, want := range wanted {
		next := -1
		for i := from; i < len(lines) && next < 0; i++ {
			next = wrappedAt(lines, i, want)
		}
		if next < 0 {
			return false
		}
		from = next
	}
	return true
}

// wrappedAt is the index after want when it starts at lines[i], -1 when it does not.
func wrappedAt(lines []string, i int, want string) int {
	got := ""
	for ; i < len(lines) && len(got) < len(want); i++ {
		got = strings.TrimSpace(got + " " + lines[i])
		if !strings.HasPrefix(want, got) {
			return -1
		}
	}
	if got != want {
		return -1
	}
	return i
}
