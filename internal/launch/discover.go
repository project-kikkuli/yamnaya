package launch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Rescan is how long a list of repositories found under a directory is reused before walking it again.
const Rescan = 24 * time.Hour

var skipped = map[string]bool{"node_modules": true, ".venv": true, "venv": true, "vendor": true, "target": true, "dist": true, "build": true}

// Discover lists the main checkouts under dir that have linked worktrees: a .git directory with a non-empty worktrees/.
// It descends into no repository, worktree, hidden directory or dependency tree.
func Discover(dir string) []string {
	var out []string
	var walk func(string)
	walk = func(d string) {
		if fi, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			if fi.IsDir() {
				if linked, _ := os.ReadDir(filepath.Join(d, ".git", "worktrees")); len(linked) > 0 {
					out = append(out, d)
				}
			}
			return
		}
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() && e.Name()[0] != '.' && !skipped[e.Name()] {
				walk(filepath.Join(d, e.Name()))
			}
		}
	}
	walk(dir)
	return out
}

type scan struct {
	Dir   string    `json:"dir"`
	At    time.Time `json:"at"`
	Repos []string  `json:"repos"`
}

// Discovered is Discover(dir), read from the cache file when it scanned dir under Rescan before now, else rescanned and saved.
func Discovered(dir, cache string, now time.Time) []string {
	var s scan
	if b, err := os.ReadFile(cache); err == nil && json.Unmarshal(b, &s) == nil && s.Dir == dir && now.Sub(s.At) < Rescan {
		return s.Repos
	}
	s = scan{Dir: dir, At: now, Repos: Discover(dir)}
	if b, err := json.Marshal(s); err == nil {
		os.WriteFile(cache, b, 0o644)
	}
	return s.Repos
}
