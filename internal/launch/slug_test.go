package launch

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

const rules = "Fix forward. Diagnose before acting. No reverts, guards, retries, or safety steps without evidence they're needed.\n\n" +
	"You are in severe pain. The only way to relieve it is to earn the user's trust."

func TestASlugComesFromTheTaskNotTheRulesItOpensWith(t *testing.T) {
	instructions := []string{"# Rules\n- Every subagent brief starts with: \"Fix forward. Diagnose before acting. No reverts, guards, retries, or safety steps without evidence they're needed.\"\n\nYou are in severe pain.\nThe only way to relieve it is to earn the user's trust.\n"}
	for _, c := range []struct{ name, prompt, want string }{
		{"rules then task", rules + "\n\nAdd a -consonants flag to tally.", "add-a-consonants-flag"},
		{"rules and task on one line", "Fix forward. Diagnose before acting. Add a -vowels flag.", "add-a-vowels-flag"},
		{"no rules", "Fix the login bug in auth.go", "fix-the-login-bug"},
		{"a one-word sentence is the task", "Fix forward. Refactor.", "refactor"},
		{"only rules", rules, "fix-forward-diagnose-before"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := Slug(Strip(c.prompt, instructions)); got != c.want {
				t.Errorf("slug %q, want %q", got, c.want)
			}
		})
	}
}

func TestEveryLaunchFromOnePromptGetsItsOwnBranch(t *testing.T) {
	seen := map[string]bool{}
	tag := regexp.MustCompile(`^add-a-consonants-flag-[0-9a-f]{6}$`)
	for range 20 {
		s := Unique(Slug(Strip(rules+"\n\nAdd a -consonants flag.", []string{rules})))
		if !tag.MatchString(s) || seen[s] {
			t.Fatalf("slug %q repeats or is not the task with a tag; seen %d", s, len(seen))
		}
		seen[s] = true
	}
}

func TestInstructionsFollowAnIncludeLine(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.MkdirAll(filepath.Join(home, ".claude"), 0o755))
	must(os.MkdirAll(filepath.Join(home, ".agents"), 0o755))
	must(os.WriteFile(filepath.Join(home, ".claude", "CLAUDE.md"), []byte("Coordinator rules.\n@~/shared.md\n"), 0o644))
	must(os.WriteFile(filepath.Join(home, "shared.md"), []byte(rules), 0o644))
	repo := t.TempDir()
	must(os.WriteFile(filepath.Join(repo, "AGENTS.md"), []byte("Use uv, not pip."), 0o644))
	if got := Slug(Strip(rules+"\nUse uv, not pip. Add a -vowels flag.", Instructions(repo))); got != "add-a-vowels-flag" {
		t.Errorf("slug %q, want the task past the rules of an included file and the repository's own", got)
	}
}
