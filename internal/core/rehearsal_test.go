package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAYmSeesARehearsalsSessionsOnlyWhenItRunsThatRehearsal(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	root := filepath.Join(base, "rehearsals")
	run := filepath.Join(root, "20261023-clean")
	for _, d := range []string{filepath.Join(run, "proving-ground-wt-x", "sub"), filepath.Join(run, "state"), filepath.Join(base, "work")} {
		os.MkdirAll(d, 0o755)
	}
	os.WriteFile(filepath.Join(root, RehearsalMark), nil, 0o644)
	owner := filepath.Join(base, "owner-state")
	for _, c := range []struct {
		name  string
		cwd   string
		state string
		seen  bool
	}{
		{"owner, session deep under the rehearsal root", filepath.Join(run, "proving-ground-wt-x", "sub"), owner, false},
		{"owner, session at the rehearsal root", root, owner, false},
		{"owner, session elsewhere", filepath.Join(base, "work"), owner, true},
		{"the rehearsal's own ym", filepath.Join(run, "proving-ground-wt-x"), filepath.Join(run, "state"), true},
		{"another rehearsal's ym under the same root", filepath.Join(run, "proving-ground-wt-x"), filepath.Join(root, "other", "state"), true},
		{"a ym whose state dir only shares the root's name as a prefix", run, root + "-other", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.core.StateDir = c.state
			w.run(10, 1, "/dev/t1", "claude", "s1", Idle)
			w.claude.sessions[10] = Session{ID: "s1", Status: Idle, Cwd: c.cwd}
			if got := len(w.board(t).Sessions) == 1; got != c.seen {
				t.Fatalf("board shows the session %v, want %v", got, c.seen)
			}
			if _, err := w.core.Find("s1"); (err == nil) != c.seen {
				t.Fatalf("Find found it %v, want %v", err == nil, c.seen)
			}
		})
	}
}
