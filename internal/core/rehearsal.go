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
	root := nearestRoot(dir)
	return root != "" && !under(stateDir, root)
}

// OutsideOwnRoot reports whether dir lies outside the nearest rehearsal or scratch root holding stateDir, or there is none: a
// scoped ym, one on a state dir other than the default, leaves such a session alone.
func OutsideOwnRoot(dir, stateDir string) bool {
	own := nearestRoot(stateDir)
	return own == "" || !under(dir, own)
}

// nearestRoot is the deepest rehearsal or scratch root holding dir, or "".
func nearestRoot(dir string) string {
	root := RehearsalRoot(dir)
	for _, r := range ScratchRoots() {
		if len(r) > len(root) && under(dir, r) {
			root = r
		}
	}
	return root
}

// DefaultStateDir reports whether dir is the owner's own state dir, ~/.local/state/yamnaya.
func DefaultStateDir(dir string) bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	want := filepath.Join(home, ".local", "state", "yamnaya")
	if filepath.Clean(dir) == want {
		return true
	}
	have, err := os.Stat(dir)
	if err != nil {
		return false
	}
	w, err := os.Stat(want)
	return err == nil && os.SameFile(have, w)
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

// foreign reports whether this ym leaves a session in dir alone: another ym's drill or test, or, for a scoped ym, anything
// outside its own root but the worktrees its own launches made.
func (c *Core) foreign(dir string) bool {
	state := c.StateDir
	if state == "" {
		if path, err := DefaultStorePath(); err == nil {
			state = filepath.Dir(path)
		}
	}
	if ForeignRehearsal(dir, state) {
		return true
	}
	if !c.Scoped || !OutsideOwnRoot(dir, state) {
		return false
	}
	if c.Worktrees == nil {
		return true
	}
	for _, wt := range c.Worktrees() {
		if wt != "" && under(dir, filepath.Clean(wt)) {
			return false
		}
	}
	return true
}
