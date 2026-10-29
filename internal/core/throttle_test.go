package core

import (
	"reflect"
	"slices"
	"sort"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/pressure"
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
			w.core.ThrottleDir = func() (string, error) { return dir, nil }
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

// holding saves a throttle that holds every ym agent when blanket, else just session, and points w at it.
func holding(t *testing.T, w *orgWorld, blanket bool, session string) {
	t.Helper()
	dir := t.TempDir()
	w.core.ThrottleDir = func() (string, error) { return dir, nil }
	th := pressure.Throttle{Blanket: blanket}
	if session != "" {
		th.Paused = []pressure.Paused{{PID: 999999, Started: 1, Command: "claude", Session: session}}
	}
	if err := th.Save(dir); err != nil {
		t.Fatal(err)
	}
}

func TestAThrottleKeepsServeFromRelaunchingADeadManager(t *testing.T) {
	for _, tc := range []struct {
		name       string
		blanket    bool
		held       func(Manager) string
		relaunched bool
	}{
		{"no throttle", false, func(Manager) string { return "" }, true},
		{"every agent held", true, func(Manager) string { return "" }, false},
		{"this manager held", false, func(m Manager) string { return m.Session }, false},
		{"another session held", false, func(Manager) string { return "elsewhere" }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			w.clock = w.clock.Add(StartGrace + time.Minute)
			w.supervise(t)
			holding(t, w, tc.blanket, tc.held(m))
			w.stop(m.Session)
			w.supervise(t)
			if got := len(w.launches.starts) == 2; got != tc.relaunched {
				t.Fatalf("%d launches, want relaunch=%v", len(w.launches.starts), tc.relaunched)
			}
		})
	}
}

func TestAThrottleKeepsServeFromResumingAnAgentWhosePaneWent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		blanket bool
		held    string
		resumed bool
	}{
		{"no throttle", false, "", true},
		{"every agent held", true, "", false},
		{"this agent held", false, "agent", false},
		{"another agent held", false, "other", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			holding(t, w, tc.blanket, tc.held)
			r.launched = append(r.launched, Launched{Key: "wt-agent", Harness: "claude", Session: "agent", State: "gone", Parent: m.Session})
			if err := w.core.ReviveWork(w.board(t)); err != nil {
				t.Fatal(err)
			}
			if got := slices.ContainsFunc(r.resumed, func(c resumeCall) bool { return c.session == "agent" }); got != tc.resumed {
				t.Fatalf("agent resumed %v, want %v", got, tc.resumed)
			}
		})
	}
}
