package launch

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var sentenceEnd = regexp.MustCompile(`[.!?]+(\s|$)|\n`)

// Unique ends slug with a random tag, so launches in other clones of the repository that started from the same prompt
// never pick the same branch before either pushes.
func Unique(slug string) string {
	b := make([]byte, 3)
	rand.Read(b)
	return slug + "-" + hex.EncodeToString(b)
}

// Strip drops the prompt's leading sentences that the agent already reads elsewhere: any of the texts in boilerplate,
// such as its instruction files or earlier launches' prompts. It keeps the whole prompt when nothing else is left.
func Strip(prompt string, boilerplate []string) string {
	var corpus strings.Builder
	for _, b := range boilerplate {
		corpus.WriteString(" " + strings.Join(slugWords(b), " ") + " ")
	}
	text := corpus.String()
	rest := prompt
	for strings.TrimSpace(rest) != "" {
		cut := len(rest)
		if loc := sentenceEnd.FindStringIndex(rest); loc != nil {
			cut = loc[1]
		}
		words := slugWords(rest[:cut])
		if len(words) > 0 && (len(words) < 2 || !strings.Contains(text, " "+strings.Join(words, " ")+" ")) {
			return rest
		}
		rest = rest[cut:]
	}
	return prompt
}

// Instructions are the instruction files an agent in repo reads on its own: the user's Claude and Codex files, the shared
// agents file and the repository's own, with the files they include by an @ line.
func Instructions(repo string) []string {
	home, _ := os.UserHomeDir()
	paths := []string{
		filepath.Join(home, ".claude", "CLAUDE.md"), filepath.Join(home, ".codex", "AGENTS.md"), filepath.Join(home, ".agents", "AGENTS.md"),
		filepath.Join(repo, "CLAUDE.md"), filepath.Join(repo, "AGENTS.md"),
	}
	seen := map[string]bool{}
	var out []string
	var read func(path string, depth int)
	read = func(path string, depth int) {
		if seen[path] {
			return
		}
		seen[path] = true
		b, err := os.ReadFile(path)
		if err != nil {
			return
		}
		out = append(out, string(b))
		if depth > 0 {
			return
		}
		for _, l := range strings.Split(string(b), "\n") {
			if inc, ok := strings.CutPrefix(strings.TrimSpace(l), "@"); ok && inc != "" {
				if strings.HasPrefix(inc, "~/") {
					inc = filepath.Join(home, inc[2:])
				} else if !filepath.IsAbs(inc) {
					inc = filepath.Join(filepath.Dir(path), inc)
				}
				read(inc, depth+1)
			}
		}
	}
	for _, p := range paths {
		read(p, 0)
	}
	return out
}
