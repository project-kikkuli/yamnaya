package cli

import (
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestReplayingAQueuedLaunchFindsItsExistingNativeRecord(t *testing.T) {
	l := testLauncher(t, t.TempDir())
	rec := launch.Record{Request: "durable-request", Key: "existing-worker", Session: "original-session", Harness: "claude", Worktree: launch.Worktree{Path: t.TempDir()}}
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	l.core = &core.Core{LaunchAdmission: func(core.NewLaunch) error { return &core.LaunchDeferred{Reason: "memory pressure"} }}
	for range 2 {
		st, err := l.Start(core.NewLaunch{Request: rec.Request, Session: "replacement-session", Prompt: "must not launch"})
		if err != nil || st.Session != rec.Session || st.Key != rec.Key {
			t.Fatalf("replay %+v, %v", st, err)
		}
	}
	all, err := l.records.All()
	if err != nil || len(all) != 1 {
		t.Fatalf("native launch duplicated: %+v, %v", all, err)
	}
}
