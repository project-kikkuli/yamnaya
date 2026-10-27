package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWireKeepsEveryFileInTheStateDirItIsGiven(t *testing.T) {
	dir, other := t.TempDir(), t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", other)
	c, _ := Wire(filepath.Join(dir, "ym.db"))
	t.Cleanup(func() { c.Store.Close() })
	c.ProfileList()
	l, ok := c.Launches.(launcher)
	if !ok {
		t.Fatalf("Wire built no launcher")
	}
	for _, p := range []string{l.records.Dir, l.conf, l.stamp, l.repos, l.landings, l.removals, l.squash.Cache, c.StateDir} {
		if p != dir && !strings.HasPrefix(p, dir+string(filepath.Separator)) {
			t.Errorf("launcher path %s lies outside %s", p, dir)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "profile-defaults.json")); err != nil {
		t.Errorf("profile defaults not kept in %s: %v", dir, err)
	}
	if left, _ := os.ReadDir(other); len(left) > 0 {
		t.Errorf("Wire wrote %v into YAMNAYA_STATE_DIR %s", left, other)
	}
}
