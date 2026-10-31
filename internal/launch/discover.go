package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Rescan is how long a list of repositories found under a directory is reused before walking it again.
const Rescan = 24 * time.Hour

var skipped = map[string]bool{"node_modules": true, ".venv": true, "venv": true, "vendor": true, "target": true, "dist": true, "build": true}

// Discover lists the main checkouts up to core.RepoDepth directories below roots that have linked worktrees: a .git
// directory with a non-empty worktrees/. It descends into no repository, worktree, hidden directory or dependency tree.
func Discover(roots []string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(string, int)
	walk = func(d string, depth int) {
		if fi, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			if fi.IsDir() && !seen[d] {
				if linked, _ := os.ReadDir(filepath.Join(d, ".git", "worktrees")); len(linked) > 0 {
					seen[d] = true
					out = append(out, d)
				}
			}
			return
		}
		if depth == core.RepoDepth {
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && e.Name()[0] != '.' && !skipped[e.Name()] {
				walk(filepath.Join(d, e.Name()), depth+1)
			}
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
	return out
}

type scan struct {
	Roots []string  `json:"roots"`
	At    time.Time `json:"at"`
	Repos []string  `json:"repos"`
}

// Discovered is Discover(roots), read from the cache file when it scanned the same roots under Rescan before now, else
// rescanned and saved.
func Discovered(roots []string, cache string, now time.Time) []string {
	var s scan
	if b, err := os.ReadFile(cache); err == nil && json.Unmarshal(b, &s) == nil && slices.Equal(s.Roots, roots) && now.Sub(s.At) < Rescan {
		return s.Repos
	}
	s = scan{Roots: roots, At: now, Repos: Discover(roots)}
	if b, err := json.Marshal(s); err == nil {
		os.WriteFile(cache, b, 0o644)
	}
	return s.Repos
}
