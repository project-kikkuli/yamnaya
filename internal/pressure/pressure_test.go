package pressure

import (
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const gib = 1 << 30

var (
	strained = Host{Memory: 24 * gib, Free: gib / 10, Level: 1, Busy: 0.95}
	calm     = Host{Memory: 24 * gib, Free: 8 * gib, Level: 1, Busy: 0.2}
)

// load is one machine reading: the host, and what the agent tree under pid 10 and an outside process (Spotlight) use.
type load struct {
	host                     Host
	agentCPU, otherCPU       float64
	agentMemory, otherMemory uint64
}

func (l load) procs() []Process {
	return []Process{
		{PID: 10, PPID: 1, Command: "claude"},
		{PID: 11, PPID: 10, Command: "go", CPU: l.agentCPU, RSS: l.agentMemory, Started: 1},
		{PID: 20, PPID: 1, Command: "mds_stores", CPU: l.otherCPU, RSS: l.otherMemory},
	}
}

var (
	memoryWarn = Host{Memory: 24 * gib, Free: gib / 10, Level: 2, Swap: 4 * gib, Busy: 0.3}
	swapFull   = Host{Memory: 24 * gib, Free: gib / 10, Level: 1, Swap: 15 * gib, Busy: 0.3}
	swapMiddle = Host{Memory: 24 * gib, Free: gib / 10, Level: 1, Swap: 8 * gib, Busy: 0.3}
	cpuFull    = Host{Memory: 24 * gib, Free: 8 * gib, Level: 1, Busy: 0.95}
	cpuMiddle  = Host{Memory: 24 * gib, Free: 8 * gib, Level: 1, Busy: 0.8}

	agentsSwap   = load{host: memoryWarn, agentCPU: 100, otherCPU: 100, agentMemory: 11 * gib, otherMemory: 9 * gib}
	agentsCPU    = load{host: cpuFull, agentCPU: 700, otherCPU: 300, agentMemory: 2 * gib, otherMemory: 4 * gib}
	spotlightRAM = load{host: memoryWarn, agentCPU: 10, otherCPU: 100, agentMemory: gib / 2, otherMemory: 20 * gib}
	spotlightCPU = load{host: cpuFull, agentCPU: 50, otherCPU: 1100, agentMemory: gib, otherMemory: 4 * gib}
	quiet        = load{host: calm, agentCPU: 600, otherCPU: 50, agentMemory: 6 * gib, otherMemory: 2 * gib}
)

func TestOnlyPressureTheAgentsCauseTripsTheThrottle(t *testing.T) {
	at := time.Unix(1000, 0)
	roots := map[int]Owner{10: {Session: "worker"}}
	for _, tc := range []struct {
		name   string
		load   load
		cause  string
		reason string
	}{
		{"agents fill memory at the kernel warning", agentsSwap, "memory", "memory: kernel pressure warn, swap 4.0 GiB, agents hold 11.0 GiB (46% of used)"},
		{"agents fill swap past half of RAM", load{host: swapFull, agentCPU: 100, otherCPU: 100, agentMemory: 10 * gib, otherMemory: 10 * gib}, "memory", "memory: kernel pressure normal, swap 15.0 GiB"},
		{"swap between the bars does not trip", load{host: swapMiddle, agentCPU: 100, otherCPU: 100, agentMemory: 10 * gib, otherMemory: 10 * gib}, "", ""},
		{"Spotlight fills memory", spotlightRAM, "", ""},
		{"agents saturate the processor", agentsCPU, "cpu", "cpu: 95% busy, agents use 70% of it"},
		{"Spotlight saturates the processor", spotlightCPU, "", ""},
		{"busy but below the trip", load{host: cpuMiddle, agentCPU: 900, otherCPU: 50}, "", ""},
		{"agents busy on a calm machine", quiet, "", ""},
		{"memory names the cause when both strain", load{host: Host{Memory: 24 * gib, Free: gib / 10, Level: 4, Busy: 0.97}, agentCPU: 900, otherCPU: 100, agentMemory: 12 * gib, otherMemory: 8 * gib}, "memory", "memory: kernel pressure critical"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s State
			for _, sec := range []int{0, 61} {
				if err := s.Sample(at.Add(time.Duration(sec)*time.Second), tc.load.host, tc.load.procs(), roots, nil, func(Process, int) error { return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if s.Holding != (tc.cause != "") || s.Cause != tc.cause || !strings.HasPrefix(s.Reason, tc.reason) {
				t.Fatalf("holding %v cause %q reason %q, want cause %q reason %q", s.Holding, s.Cause, s.Reason, tc.cause, tc.reason)
			}
			if status := s.Status(0); tc.cause != "" && !strings.Contains(status, "throttle ("+tc.cause+")") || !strings.Contains(status, tc.reason) {
				t.Fatalf("status %q", status)
			}
		})
	}
}

func TestSustainedPressureHoldsSlotsAndRecoveryUsesHysteresis(t *testing.T) {
	at := time.Unix(1000, 0)
	middle := load{host: swapMiddle, agentCPU: 100, otherCPU: 100, agentMemory: 10 * gib, otherMemory: 10 * gib}
	type step struct {
		seconds int
		load    load
	}
	for _, tc := range []struct {
		name            string
		steps           []step
		active, holding bool
	}{
		{"brief spike", []step{{0, agentsSwap}, {59, agentsSwap}}, false, false},
		{"sustained", []step{{0, agentsSwap}, {61, agentsSwap}}, true, true},
		{"interrupted", []step{{0, agentsSwap}, {45, middle}, {61, agentsSwap}}, false, false},
		{"hold through the middle band", []step{{0, agentsSwap}, {61, agentsSwap}, {62, middle}}, true, true},
		{"open slots once pressure ends", []step{{0, agentsSwap}, {61, agentsSwap}, {62, quiet}}, true, false},
		{"open slots once Spotlight is the cause", []step{{0, agentsCPU}, {61, agentsCPU}, {62, spotlightCPU}}, true, false},
		{"recovery interrupted", []step{{0, agentsSwap}, {61, agentsSwap}, {62, quiet}, {200, middle}, {243, quiet}}, true, false},
		{"recovered", []step{{0, agentsSwap}, {61, agentsSwap}, {62, quiet}, {243, quiet}}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s State
			for _, p := range tc.steps {
				if err := s.Sample(at.Add(time.Duration(p.seconds)*time.Second), p.load.host, p.load.procs(), map[int]Owner{10: {Session: "worker"}}, nil, func(Process, int) error { return nil }); err != nil {
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
		{PID: 23, PPID: 11, Command: "realtime", CPU: 1000, NiceUnavailable: true},
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
	if err := s.Sample(at, strained, processes, roots, nil, setter); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatal("reniced before sustained overload", got)
	}
	if err := s.Sample(at.Add(61*time.Second), strained, processes, roots, nil, setter); err != nil {
		t.Fatal(err)
	}
	if want := []int{21, 13, 19, 12, 14}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reniced %v, want %v", got, want)
	}
	if len(s.Notices(1)) != 2 || !strings.Contains(s.Notices(1)[0].Text, "python3 pid 13 70%") {
		t.Fatal(s.Notices(1))
	}
	s.Notified["goal1"] = true
	s.Notified["goal2"] = true
	if len(s.Notices(1)) != 0 {
		t.Fatal("duplicate notices", s.Notices(1))
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
	if err := restored.Sample(at.Add(70*time.Second), strained, processes, roots, nil, setter); err != nil {
		t.Fatal(err)
	}
	if len(got) > 0 || len(restored.Notices(1)) > 0 || !restored.Holding {
		t.Fatal("restart repeated work or lost hold", got, restored)
	}
}

func TestExitedJobsAndReusedPIDsDoNotKeepOriginalPriorities(t *testing.T) {
	at := time.Unix(1000, 0)
	state := State{Active: true, Holding: true, Changed: []Change{{Process: Process{PID: 12, Started: 100}}, {Process: Process{PID: 13, Started: 100}}}}
	if err := state.Sample(at, calm, []Process{{PID: 12, Started: 200}}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if len(state.Changed) != 0 {
		t.Fatal(state.Changed)
	}
}

func TestPriorityFailuresAreReportedAndNeverClaimedAsApplied(t *testing.T) {
	state := State{Active: true}
	err := state.Sample(time.Now(), strained, []Process{{PID: 10, Command: "codex"}, {PID: 11, PPID: 10, Command: "compile", CPU: 50}}, map[int]Owner{10: {Session: "worker"}}, nil, func(Process, int) error { return os.ErrPermission })
	if !errors.Is(err, os.ErrPermission) || len(state.Changed) != 0 {
		t.Fatal(err, state.Changed)
	}
}

func TestProcessTableKeepsAncestryPriorityCPUAndIdentity(t *testing.T) {
	ps, err := parseProcesses("12 10 10 71.3 2048 Fri Oct 23 16:30:22 2026 /tmp/a compiler\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(ps) != 1 || ps[0].PID != 12 || ps[0].PPID != 10 || ps[0].CPU != 71.3 || ps[0].RSS != 2<<20 || ps[0].Nice != 10 || ps[0].Command != "/tmp/a compiler" || ps[0].Started == 0 {
		t.Fatal(ps)
	}
	if _, err := parseProcesses("12 10 10\n"); err == nil {
		t.Fatal("accepted truncated ps output")
	}
}

func TestLinuxKernelRowsWithoutNiceValuesPreserveAncestryAndDoNotAbortTheSample(t *testing.T) {
	raw := "22 2 - 0.0 0 Fri Oct 16 01:44:04 2026 migration/0\n25 2 - 0.0 0 Fri Oct 16 01:44:04 2026 migration/1\n827004 2 - 0.0 0 Fri Oct 23 15:50:47 2026 psimon\n12 10 10 71.3 900 Fri Oct 23 16:30:22 2026 /bin/compile\n"
	procs, err := parseProcesses(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(procs) != 4 || !procs[0].NiceUnavailable || procs[0].PID != 22 || procs[0].PPID != 2 || procs[3].NiceUnavailable || procs[3].Nice != 10 {
		t.Fatal(procs)
	}
}

func TestThrottleLinesTellWhetherTheNextSlotIsGranted(t *testing.T) {
	holding := State{Active: true, Holding: true, Cause: "cpu", Reason: "cpu: 97% busy, agents use 80% of it", Changed: []Change{{Process: Process{PID: 7, CPU: 90, Command: "go"}, Owner: Owner{Goal: "g", Manager: "m"}}}}
	for _, c := range []struct {
		name        string
		state       State
		held        int
		line, slots string
	}{
		{"idle pool", holding, 0, "throttle (cpu): heavy agent jobs nice 15 (1 live), 0 slots held, next slot granted", "slots open"},
		{"busy pool", holding, 2, "throttle (cpu): heavy agent jobs nice 15 (1 live), 2 slots held, new slots wait", "slots held"},
		{"recovering", State{Active: true}, 2, "throttle: recovering, new slots open; clears after 3 minutes without agent pressure", ""},
	} {
		if got := c.state.Line(c.held); got != c.line {
			t.Errorf("%s: line %q, want %q", c.name, got, c.line)
		}
		if notices := c.state.Notices(c.held); c.slots != "" && (len(notices) != 1 || !strings.Contains(notices[0].Text, "cpu: 97% busy, agents use 80% of it; child jobs nice 15; "+c.slots+";")) {
			t.Errorf("%s: notices %+v, want %q", c.name, notices, c.slots)
		}
	}
}
