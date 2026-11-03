package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// ClaudeConfigFile is the file Claude Code keeps its global config in for config root: the environment's root when root is "",
// the home directory's .claude.json for the default root no CLAUDE_CONFIG_DIR names, and a non-empty legacy .config.json over either.
func ClaudeConfigFile(root string, explicit bool) (string, error) {
	if root == "" {
		root = os.Getenv("CLAUDE_CONFIG_DIR")
		explicit = root != ""
	}
	path := filepath.Join(root, ".claude.json")
	if !explicit {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, ".claude.json")
		if root == "" {
			root = filepath.Join(home, ".claude")
		}
	}
	legacy := filepath.Join(root, ".config.json")
	if info, err := os.Stat(legacy); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return legacy, nil
	}
	return path, nil
}

// EnsureOnboardingComplete records Claude Code's first-run and Chrome onboarding as done in config root's .claude.json, as finishing
// them does, so a session started there opens at its prompt instead of the theme picker, security notes or Chrome modal.
func EnsureOnboardingComplete(root string, explicit bool) error {
	path, err := ClaudeConfigFile(root, explicit)
	if err != nil {
		return err
	}
	return updateClaudeConfig(path, func(config map[string]json.RawMessage) (bool, error) {
		changed := false
		for _, key := range []string{"hasCompletedOnboarding", "hasCompletedClaudeInChromeOnboarding"} {
			if string(config[key]) != "true" {
				config[key] = json.RawMessage("true")
				changed = true
			}
		}
		return changed, nil
	})
}
