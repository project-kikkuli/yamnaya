package cli

import (
	"github.com/project-kikkuli/yamnaya/internal/pressure"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestOverloadRootsRequireALiveYMPaneAndResolveGoalAncestry(t *testing.T) {
	records := []launch.Record{
		{Key: "manager", Session: "manager"},
		{Key: "agent", Session: "agent", Parent: "manager"},
		{Key: "child", Session: "child", Parent: "agent"},
		{Key: "gone", Session: "gone", Parent: "manager"},
		{Key: "seen", Session: "seen", Seen: time.Now()},
		{Key: "landed", Session: "landed", Landed: time.Now()},
		{Key: "dead", Session: "dead"},
	}
	panes := map[string]tmux.Pane{}
	for i, r := range records {
		panes[r.Key] = tmux.Pane{PID: 100 + i, Dead: r.Key == "dead"}
	}
	delete(panes, "gone")
	panes["owner-terminal"] = tmux.Pane{PID: 200}
	org := core.Org{Managers: map[string]core.Manager{"mgr1": {ID: "mgr1", Session: "manager", GoalID: "goal1"}}, Sessions: map[string]string{"manager": "mgr1"}}
	roots := pressureRoots(records, panes, org)
	if len(roots) != 3 {
		t.Fatal(roots)
	}
	for _, pid := range []int{100, 101, 102} {
		if roots[pid].Goal != "goal1" || roots[pid].Manager != "manager" {
			t.Fatal(roots)
		}
	}
}

func TestOverloadNoticeSurvivesARestartBetweenRecordingAndSavingState(t *testing.T) {
	dir := t.TempDir()
	store, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	c := &core.Core{Store: store}
	state := pressure.State{Active: true, Holding: true, Since: time.Unix(1000, 0), Load: 30, Cores: 12,
		Notified: map[string]bool{}, Changed: []pressure.Change{{Process: pressure.Process{PID: 11, Command: "compile", CPU: 80}, Owner: pressure.Owner{Goal: "goal1", Manager: "manager"}}}}
	if err := recordPressureNotices(c, &state, dir); err != nil {
		t.Fatal(err)
	}
	state.Notified = map[string]bool{}
	if err := recordPressureNotices(c, &state, dir); err != nil {
		t.Fatal(err)
	}
	events, err := store.GoalEvents("goal1")
	if err != nil || len(events) != 1 || !events[0].Delivered.IsZero() {
		t.Fatal(events, err)
	}
}
