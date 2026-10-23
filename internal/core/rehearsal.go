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

// ForeignRehearsal reports whether dir lies under a rehearsal root that stateDir does not: a rehearsal's sessions belong to the
// ym running it, so every other ym leaves them undiscovered, unrecorded and unadopted.
func ForeignRehearsal(dir, stateDir string) bool {
	root := RehearsalRoot(dir)
	if root == "" {
		return false
	}
	s := filepath.Clean(stateDir)
	return s != root && !strings.HasPrefix(s, root+string(filepath.Separator))
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
