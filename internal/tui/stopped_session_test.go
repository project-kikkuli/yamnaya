package tui

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestArchivingADeadLaunchHidesItWithoutCleaningItsWorktree(t *testing.T) {
	m, c, _ := setup(t)
	l := &launches{dormant: []core.Dormant{{Key: "old", Harness: "claude", Session: "gone", Title: "Caller migration", State: "unpushed changes"}}}
	c.Launches = l
	c.Procs = func() ([]proc.Proc, error) { return nil, nil }
	m = refresh(t, m)
	m = to(t, m, func(r row) bool { return r.project != nil && r.need == nil && len(r.project.Dormant) == 1 })
	m = press(t, m, "K", "y")
	for _, p := range m.board.Projects {
		if len(p.Dormant) > 0 && p.Shown() {
			t.Fatal("archiving left the dead launch on the board")
		}
	}
	if len(l.landed) != 0 || len(l.forgot) != 0 {
		t.Fatal("archive invoked worktree cleanup")
	}
}
