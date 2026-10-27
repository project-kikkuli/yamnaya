package core

import (
	"os"
	"path/filepath"
	"strings"
)

// RehearsalMark names the file that marks a directory as the root of rehearsal runs.
const RehearsalMark = ".ym-rehearsal"

// RehearsalRoot is the nearest directory at or above dir holding RehearsalMark, or "".
func RehearsalRoot(dir string) string {
	if dir == "" {
		return ""
	}
	for d := filepath.Clean(dir); ; d = filepath.Dir(d) {
		if _, err := os.Stat(filepath.Join(d, RehearsalMark)); err == nil {
			return d
		}
		if filepath.Dir(d) == d {
			return ""
		}
	}
}

// ForeignRehearsal reports whether the nearest rehearsal or scratch root holding dir does not hold stateDir: a drill's or test's
// sessions belong to the ym running it, so every other ym leaves them undiscovered, unrecorded and unadopted.
func ForeignRehearsal(dir, stateDir string) bool {
	root := RehearsalRoot(dir)
	for _, r := range ScratchRoots() {
		if len(r) > len(root) && under(dir, r) {
			root = r
		}
	}
	return root != "" && !under(stateDir, root)
}

// ScratchRoots are the directories drills and tests work in whether or not they mark them: the system temporary directories and ~/tmp/arbiter-work.
func ScratchRoots() []string {
	roots := []string{"/tmp", "/private/tmp", "/var/folders", "/private/var/folders"}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, "tmp", "arbiter-work"))
	}
	return roots
}

func under(dir, root string) bool {
	d := filepath.Clean(dir)
	return d == root || strings.HasPrefix(d, root+string(filepath.Separator))
}

// foreign is ForeignRehearsal for this ym's state dir.
func (c *Core) foreign(dir string) bool {
	state := c.StateDir
	if state == "" {
		if path, err := DefaultStorePath(); err == nil {
			state = filepath.Dir(path)
		}
	}
	return ForeignRehearsal(dir, state)
}
