package core

import (
	"reflect"
	"sort"
	"syscall"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/pressure"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func TestThrottlePausesTheLocalYmAgentsInScopeAndResumesThem(t *testing.T) {
	for _, tc := range []struct {
		name    string
		scope   func(m Manager) *OffTarget
		paused  func(m Manager) []string
		resumes func(m Manager) *OffTarget
		left    func(m Manager) []string
	}{
		{"every agent", all, sessions("agent", "stray"), all, sessions()},
		{"one goal", goal, sessions("agent"), all, sessions()},
		{"one session", func(Manager) *OffTarget { return &OffTarget{Session: "stray"} }, func(Manager) []string { return []string{"stray"} }, all, sessions()},
		{"resume one goal of all", all, sessions("agent", "stray"), goal, func(Manager) []string { return []string{"stray"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
			w.spawn("agent", m.Session)
			w.spawn("stray", "")
			w.spawn("remote", "")
			w.spawn("foreign", "")
			dir := t.TempDir()
			w.core.Slots = func() (slot.Pool, error) { return slot.Pool{Dir: dir, Capacity: 1, Cores: 8}, nil }
			b, err := w.core.Board()
			if err != nil {
				t.Fatal(err)
			}
			var procs []pressure.Process
			for i, s := range b.Sessions {
				b.Sessions[i].Ours = s.ID != "foreign"
				if s.ID == "remote" {
					b.Sessions[i].Remote = "devbox"
				}
				procs = append(procs, pressure.Process{PID: s.PID, PPID: 1, Command: "claude", Started: 1},
					pressure.Process{PID: s.PID + 5000, PPID: s.PID, Command: "go", Started: 1})
			}
			signalled := map[int]syscall.Signal{}
			w.core.Processes = func() ([]pressure.Process, error) { return procs, nil }
			w.core.ThrottleSignal = func(p pressure.Process, sig syscall.Signal) error {
				signalled[p.PID] = sig
				return nil
			}
			if _, err := w.core.Throttle(b, tc.scope(m), true); err != nil {
				t.Fatal(err)
			}
			st, err := pressure.ReadThrottle(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got := st.Sessions(); !reflect.DeepEqual(got, tc.paused(m)) {
				t.Fatalf("paused %q, want %q", got, tc.paused(m))
			}
			if len(signalled) != 2*len(tc.paused(m)) {
				t.Fatalf("signalled %v, want each paused agent and its child", signalled)
			}
			if _, err := w.core.Throttle(b, tc.resumes(m), false); err != nil {
				t.Fatal(err)
			}
			if st, err = pressure.ReadThrottle(dir); err != nil {
				t.Fatal(err)
			}
			got := st.Sessions()
			sort.Strings(got)
			if !reflect.DeepEqual(got, tc.left(m)) {
				t.Fatalf("still paused %q, want %q", got, tc.left(m))
			}
			for _, p := range st.Paused {
				if signalled[p.PID] != syscall.SIGSTOP {
					t.Fatalf("pid %d still paused but last got %v", p.PID, signalled[p.PID])
				}
			}
		})
	}
}

func all(Manager) *OffTarget    { return nil }
func goal(m Manager) *OffTarget { return &OffTarget{Manager: &m} }

// sessions are the named sessions with the goal's manager, sorted; none names nothing at all.
func sessions(names ...string) func(Manager) []string {
	return func(m Manager) []string {
		if len(names) == 0 {
			return nil
		}
		out := append([]string{m.Session}, names...)
		sort.Strings(out)
		return out
	}
}
