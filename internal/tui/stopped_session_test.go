package tui

import (
	"slices"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestKillingADeadLaunchEndsItAndLeavesTheBoard(t *testing.T) {
	m, c, _ := setup(t)
	l := &launches{dormant: []core.Dormant{{Key: "old", Harness: "claude", Session: "gone", Title: "Caller migration", State: "unpushed changes"}}}
	c.Launches = l
	c.Procs = func() ([]proc.Proc, error) { return nil, nil }
	m = refresh(t, m)
	m = to(t, m, func(r row) bool { return r.project != nil && r.need == nil && len(r.project.Dormant) == 1 })
	press(t, m, "K", "y")
	if !slices.Equal(l.landed, []string{"gone"}) {
		t.Fatalf("kill ended %v, want the dead launch's session", l.landed)
	}
}
