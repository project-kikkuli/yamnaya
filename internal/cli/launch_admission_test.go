package cli

import (
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
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

func TestMachineSaturationQueuesLaunchesWithoutTreatingHistoricalSwapAsPressure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		host      pressure.Host
		cores     int
		load      float64
		throttled bool
		want      string
	}{
		{name: "available", host: pressure.Host{Level: 1}, cores: 6, load: 3},
		{name: "memory warn", host: pressure.Host{Level: 2}, cores: 6, load: 3, want: "memory pressure"},
		{name: "memory critical", host: pressure.Host{Level: 4}, cores: 6, load: 3, want: "memory pressure"},
		{name: "historical swap", host: pressure.Host{Level: 1, Memory: 24 << 30, Swap: 14 << 30}, cores: 6, load: 3},
		{name: "all cores occupied", host: pressure.Host{Level: 1}, cores: 6, load: 6, want: "processor load"},
		{name: "overloaded", host: pressure.Host{Level: 1}, cores: 6, load: 18, want: "processor load"},
		{name: "owner throttle", host: pressure.Host{Level: 1}, cores: 6, load: 3, throttled: true, want: "throttle"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := launchMachineReason(tc.host, tc.cores, tc.load, tc.throttled)
			if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("capacity %q, want %q", got, tc.want)
			}
		})
	}
	for _, free := range []uint64{0, 9, 10, 30} {
		got := launchFilesystemReason(disk.Usage{Total: 100, Avail: free})
		if (got != "") != (free < 10) {
			t.Fatalf("%d%% free: %q", free, got)
		}
	}
}
