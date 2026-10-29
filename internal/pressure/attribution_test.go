package pressure

import (
	"reflect"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestAppServerJobsUseThreadIdentityAndWorktreeBoundariesWithoutClaimingOtherTerminals(t *testing.T) {
	owner := Owner{Session: "worker", Goal: "goal1", Manager: "mgr1"}
	for _, tc := range []struct {
		name     string
		identity Identity
		parent   int
		want     bool
	}{
		{"thread outside its worktree", Identity{Thread: "worker", Cwd: "/tmp"}, 10, true},
		{"worktree root", Identity{Cwd: "/repo/worker"}, 10, true},
		{"worktree descendant", Identity{Cwd: "/repo/worker/subdir"}, 10, true},
		{"similar directory is not a child", Identity{Cwd: "/repo/worker-other"}, 10, false},
		{"unknown thread elsewhere", Identity{Thread: "unmanaged", Cwd: "/tmp"}, 10, false},
		{"owner terminal in same directory", Identity{Cwd: "/repo/worker"}, 20, false},
		{"detached maintenance", Identity{Thread: "worker", Cwd: "/repo/worker"}, 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := []Process{{PID: 10, PPID: 1, Command: "codex", CPU: 80}, {PID: 20, PPID: 1, Command: "Terminal"}, {PID: 30, PPID: tc.parent, Command: "compile", CPU: 60}}
			jobs := AppJobs(ps, map[int]bool{10: true}, map[string]Owner{"worker": owner}, map[string]Owner{"/repo/worker": owner}, func(int) Identity { return tc.identity })
			if got := jobs[30] == owner; got != tc.want {
				t.Fatal(jobs)
			}
			if _, ok := jobs[10]; ok {
				t.Fatal("claimed shared app-server", jobs)
			}
		})
	}
}

func TestAppServerToolChildrenAndTheirJobChildrenAreNicedWithoutChangingTheirShell(t *testing.T) {
	owner := Owner{Session: "worker", Goal: "goal1", Manager: "mgr1"}
	ps := []Process{
		{PID: 10, PPID: 1, Command: "codex", CPU: 80},
		{PID: 20, PPID: 10, Command: "zsh", CPU: 5},
		{PID: 30, PPID: 20, Command: "go", CPU: 60},
		{PID: 31, PPID: 30, Command: "compile", CPU: 0},
		{PID: 40, PPID: 10, Command: "compile", CPU: 70},
	}
	jobs := AppJobs(ps, map[int]bool{10: true}, map[string]Owner{"worker": owner}, nil, func(pid int) Identity {
		if pid == 20 {
			return Identity{Thread: "worker"}
		}
		return Identity{}
	})
	state := State{Active: true}
	var got []int
	if err := state.Sample(time.Now(), strained, ps, nil, jobs, func(p Process, _ int) error { got = append(got, p.PID); return nil }); err != nil {
		t.Fatal(err)
	}
	if want := []int{30, 31}; !reflect.DeepEqual(got, want) {
		t.Fatalf("niced %v, want %v", got, want)
	}
	if len(state.Notices(1)) != 1 {
		t.Fatal(state.Notices(1))
	}
}

func TestAppServerDetectionRequiresTheCodexSubcommand(t *testing.T) {
	got := AppServers([]proc.Proc{
		{PID: 10, Args: []string{"/bin/codex", "app-server", "--listen", "unix://"}},
		{PID: 20, Args: []string{"/bin/codex", "--yolo", "help me with an app-server"}},
		{PID: 30, Args: []string{"/bin/not-codex", "app-server"}},
	})
	if !reflect.DeepEqual(got, map[int]bool{10: true}) {
		t.Fatal(got)
	}
}
