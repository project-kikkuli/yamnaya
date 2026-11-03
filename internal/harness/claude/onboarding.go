package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness/screen"
)

// onboardingSteps are the screens of Claude Code's first-run onboarding, each told apart by lines only it draws.
var onboardingSteps = []struct {
	step  string
	lines []string
}{
	{"theme", []string{"Let's get started.", "Choose the text style that looks best with your terminal"}},
	{"api key", []string{"Detected a custom API key in your environment", "Do you want to use this API key?"}},
	{"login", []string{"Select login method:", "Claude account with subscription"}},
	{"security notes", []string{"Security notes:", "Press Enter to continue"}},
	{"terminal setup", []string{"Use Claude Code's terminal setup?", "No, maybe later with /terminal-setup"}},
	{"chrome", []string{"Requires the Chrome extension.", "Enter to confirm · Esc to cancel"}},
}

// Onboarding reports the first-run onboarding step the screen shows, which hides the input box until the owner finishes it.
func (a *Adapter) Onboarding(text string) (string, bool) { return ParseOnboarding(text) }

func ParseOnboarding(text string) (string, bool) {
	if _, ok := Composer(text); ok {
		return "", false
	}
	lines := screen.Lines(text)
	for _, s := range onboardingSteps {
		all := true
		for _, want := range s.lines {
			if !screen.Has(lines, want) {
				all = false
				break
			}
		}
		if all {
			return s.step, true
		}
	}
	return "", false
}

// onboardingState is what the config file last said about onboarding, kept until the file changes.
type onboardingState struct {
	size     int64
	modified time.Time
	read     bool
	done     bool
}

// onboarded reports whether the config marks first-run onboarding done; until it does, every session started on it opens on
// the onboarding screens, so its screen is worth reading.
func (a *Adapter) onboarded() bool {
	path := a.Config
	if path == "" {
		path = filepath.Join(a.Dir, ".claude.json")
	}
	info, err := a.stat(path)
	a.files.Lock()
	defer a.files.Unlock()
	was := a.setup
	switch {
	case err != nil:
		a.setup = onboardingState{read: true}
	case !was.read || was.size != info.Size() || !was.modified.Equal(info.ModTime()):
		var c struct {
			Done bool `json:"hasCompletedOnboarding"`
		}
		b, rerr := os.ReadFile(path)
		a.setup = onboardingState{size: info.Size(), modified: info.ModTime(), read: true, done: rerr == nil && json.Unmarshal(b, &c) == nil && c.Done}
	}
	if was.read && was.done != a.setup.done {
		a.gen.Add(1)
	}
	return a.setup.done
}
