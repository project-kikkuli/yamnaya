package pressure

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestSustainedLoadHoldsSlotsAndRecoveryUsesHysteresis(t *testing.T) {
	at := time.Unix(1000, 0)
	for _, tc := range []struct {
		name    string
		samples []struct {
			seconds int
			load    float64
		}
		active, holding bool
	}{
		{"brief spike", []struct {
			seconds int
			load    float64
		}{{0, 25}, {59, 25}}, false, false},
		{"sustained overload", []struct {
			seconds int
			load    float64
		}{{0, 25}, {61, 25}}, true, true},
		{"interrupted overload", []struct {
			seconds int
			load    float64
		}{{0, 25}, {45, 24}, {61, 25}}, false, false},
		{"hold through middle band", []struct {
			seconds int
			load    float64
		}{{0, 25}, {61, 25}, {62, 18}}, true, true},
		{"open slots below low threshold", []struct {
			seconds int
			load    float64
		}{{0, 25}, {61, 25}, {62, 14}}, true, false},
		{"recovery interrupted", []struct {
			seconds int
			load    float64
		}{{0, 25}, {61, 25}, {62, 14}, {200, 15}, {243, 14}}, true, false},
		{"recovered", []struct {
			seconds int
			load    float64
		}{{0, 25}, {61, 25}, {62, 14}, {243, 14}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s State
			for _, p := range tc.samples {
				if err := s.Sample(at.Add(time.Duration(p.seconds)*time.Second), p.load, 12, nil, nil, nil, nil); err != nil {
					t.Fatal(err)
				}
			}
			if s.Active != tc.active || s.Holding != tc.holding {
				t.Fatalf("active/holding %v/%v, want %v/%v", s.Active, s.Holding, tc.active, tc.holding)
			}
		})
	}
}

func TestOnlyHeavyAgentChildTreesAreNicedHeaviestFirst(t *testing.T) {
	at := time.Unix(1000, 0)
	processes := []Process{
		{PID: 1, Command: "launchd"},
		{PID: 2, PPID: 1, Command: "WindowServer", CPU: 80},
		{PID: 3, PPID: 1, Command: "Terminal", CPU: 5},
		{PID: 4, PPID: 3, Command: "go", CPU: 200},
		{PID: 10, PPID: 1, Command: "codex", CPU: 30},
		{PID: 11, PPID: 10, Command: "zsh", CPU: 3},
		{PID: 12, PPID: 11, Command: "git", CPU: 20, Nice: 10, Started: 100},
		{PID: 13, PPID: 12, Command: "python3", CPU: 70, Nice: 0, Started: 100},
		{PID: 14, PPID: 13, Command: "helper", CPU: 0, Nice: 0, Started: 100},
		{PID: 15, PPID: 11, Command: "go", CPU: 0},
		{PID: 16, PPID: 11, Command: "go", CPU: 100, Nice: 19},
		{PID: 17, PPID: 11, Command: "claude.exe", CPU: 40},
		{PID: 18, PPID: 11, Command: "ym", CPU: 10},
		{PID: 19, PPID: 18, Command: "go", CPU: 40, Started: 100},
		{PID: 20, PPID: 1, Command: "claude", CPU: 10},
		{PID: 21, PPID: 20, Command: "compile", CPU: 90, Started: 100},
		{PID: 22, PPID: 11, Command: "tmux", CPU: 1},
		{PID: 30, PPID: 31, Command: "compile", CPU: 100},
		{PID: 31, PPID: 30, Command: "compile", CPU: 100},
	}
	roots := map[int]Owner{10: {Session: "worker", Goal: "goal1", Manager: "mgr1"}, 20: {Session: "other", Goal: "goal2", Manager: "mgr2"}}
	var got []int
	setter := func(p Process, n int) error {
		if n != 15 {
			t.Fatal(n)
		}
		got = append(got, p.PID)
		return nil
	}
	var s State
	if err := s.Sample(at, 30, 12, processes, roots, nil, setter); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("reniced before sustained overload", got)
	}
	if err := s.Sample(at.Add(61*time.Second), 30, 12, processes, roots, nil, setter); err != nil {
		t.Fatal(err)
	}
	if want := []int{21, 13, 19, 12, 14}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reniced %v, want %v", got, want)
	}
	if len(s.Notices()) != 2 || !strings.Contains(s.Notices()[0].Text, "python3 pid 13 70%") {
		t.Fatal(s.Notices())
	}
	s.Notified["goal1"] = true
	s.Notified["goal2"] = true
	if len(s.Notices()) != 0 {
		t.Fatal("duplicate notices", s.Notices())
	}
	dir := t.TempDir()
	if err := s.Save(dir); err != nil {
		t.Fatal(err)
	}
	restored, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	got = nil
	if err := restored.Sample(at.Add(70*time.Second), 30, 12, processes, roots, nil, setter); err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 || len(restored.Notices()) > 0 || !restored.Holding {
		t.Fatal("restart repeated work or lost hold", got, restored)
	}
}

func TestExitedJobsAndReusedPIDsDoNotKeepOriginalPriorities(t *testing.T) {
	at := time.Unix(1000, 0)
	state := State{Active: true, Holding: true, Changed: []Change{{Process: Process{PID: 12, Started: 100}}, {Process: Process{PID: 13, Started: 100}}}}
	if err := state.Sample(at, 10, 12, []Process{{PID: 12, Started: 200}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(state.Changed) != 0 {
		t.Fatal(state.Changed)
	}
}

func TestPriorityFailuresAreReportedAndNeverClaimedAsApplied(t *testing.T) {
	state := State{Active: true}
	err := state.Sample(time.Now(), 30, 12, []Process{{PID: 10, Command: "codex"}, {PID: 11, PPID: 10, Command: "compile", CPU: 50}}, map[int]Owner{10: {Session: "worker"}}, nil, func(Process, int) error { return os.ErrPermission })
	if !errors.Is(err, os.ErrPermission) || len(state.Changed) != 0 {
		t.Fatal(err, state.Changed)
	}
}

func TestProcessTableKeepsAncestryPriorityCPUAndIdentity(t *testing.T) {
	ps, err := parseProcesses("12 10 10 71.3 Fri Oct 23 16:30:22 2026 /tmp/a compiler\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].PID != 12 || ps[0].PPID != 10 || ps[0].CPU != 71.3 || ps[0].Nice != 10 || ps[0].Command != "/tmp/a compiler" || ps[0].Started == 0 {
		t.Fatal(ps)
	}
	if _, err := parseProcesses("12 10 10\n"); err == nil {
		t.Fatal("accepted truncated ps output")
	}
}
