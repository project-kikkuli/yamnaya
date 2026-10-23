package core

import (
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func TestAHungAgentReachesItsManagerOnceAndStopsCountingAsMovement(t *testing.T) {
	for _, c := range []struct {
		name     string
		doing    time.Duration
		crew     time.Duration
		woke     time.Duration
		wantHung bool
	}{
		{"last tool call long ago", -20 * time.Minute, 0, 0, true},
		{"a long tool call", -9 * time.Minute, 0, 0, false},
		{"a subagent still working", -40 * time.Minute, -2 * time.Minute, 0, false},
		{"silent mostly while the host slept", -40 * time.Minute, 0, -5 * time.Minute, false},
		{"silent long after the host woke", -40 * time.Minute, 0, -16 * time.Minute, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "make the importer handle CSV", "/repo/august")
			w.spawn("agent", m.Session)
			w.clock = w.clock.Add(time.Hour)
			if c.woke != 0 {
				w.core.Woke = w.clock.Add(c.woke)
			}
			w.set("agent", func(s *Session) {
				s.Status, s.Since = Busy, w.clock.Add(-45*time.Minute)
				s.Doing = Action{Tool: "Bash", Detail: "go test ./...", At: w.clock.Add(c.doing)}
				if c.crew != 0 {
					s.Crew = []Member{{ID: "sub", Status: Busy, Last: w.clock.Add(c.crew)}}
				}
			})
			w.loop(t, 3)
			var got []string
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, "looks hung") {
					got = append(got, text)
				}
			}
			if c.wantHung != (len(got) == 1) || len(got) > 1 {
				t.Fatalf("manager heard %q, want hung=%v once", got, c.wantHung)
			}
			if c.wantHung && !strings.Contains(got[0], "go test ./...") {
				t.Fatalf("notice %q lacks what the agent last did", got[0])
			}
			moved, _ := w.core.Store.Goal(g.ID)
			if c.wantHung == moved.LastMovedAt.Equal(w.clock) {
				t.Fatalf("goal last moved %v at clock %v; a hung agent must not count as movement, a working one must", moved.LastMovedAt, w.clock)
			}
		})
	}
}

func TestAHungManagerIsEndedAndRelaunched(t *testing.T) {
	for _, c := range []struct {
		name         string
		woke         time.Duration
		wantRelaunch bool
	}{
		{"silent while awake", 0, true},
		{"silent mostly while the host slept", -2 * time.Minute, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
			w.core.Signal = func(pid int, _ syscall.Signal) error {
				w.stop(w.claude.sessions[pid].ID)
				return nil
			}
			w.clock = w.clock.Add(time.Hour)
			if c.woke != 0 {
				w.core.Woke = w.clock.Add(c.woke)
			}
			w.set(m.Session, func(s *Session) {
				s.Status, s.Since = Busy, w.clock.Add(-30*time.Minute)
				s.Doing = Action{Tool: "Bash", At: w.clock.Add(-20 * time.Minute)}
			})
			w.supervise(t)
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			again := o.Managers[m.ID]
			if relaunched := again.Session != m.Session && len(w.launches.starts) == 2; relaunched != c.wantRelaunch {
				t.Fatalf("manager session %s -> %s after %d launches, want relaunch=%v", m.Session, again.Session, len(w.launches.starts), c.wantRelaunch)
			}
			if c.wantRelaunch && !strings.Contains(w.launches.starts[1].Prompt, "relaunched") {
				t.Fatalf("relaunch prompt lacks the resume note:\n%s", w.launches.starts[1].Prompt)
			}
		})
	}
}

func TestAProcessHoldingALargeShareOfMemoryReachesItsAgentsManagerOnce(t *testing.T) {
	const gib = 1 << 30
	for _, c := range []struct {
		name  string
		bytes float64
		want  string
	}{
		{"leaked helper under an agent", 26 * gib, "memory: script pid 5000 holds 26 GiB, 41% of RAM, for 51m"},
		{"an ordinary agent-sized process", 0.4 * gib, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "keep workstations current", "/repo/vibe-apps")
			agent := 1000 + len(w.procs)
			w.spawn("agent", m.Session)
			w.procs = append(w.procs, proc.Proc{PID: 5000, PPID: agent, Elapsed: 51 * time.Minute, Args: []string{"script", "-qc", "claude", "/dev/null"}})
			w.core.Slots = func() (slot.Pool, error) { return slot.Pool{Dir: t.TempDir(), Capacity: 3, Cores: 8, Memory: 64 * gib}, nil }
			w.core.Loads = func() float64 { return 1 }
			w.core.Mem = func() (map[int]float64, error) { return map[int]float64{agent: 0.3 * gib, 5000: c.bytes}, nil }
			w.loop(t, 3)
			var got []string
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, "memory:") {
					got = append(got, text)
				}
			}
			if c.want == "" {
				if len(got) != 0 {
					t.Fatalf("manager heard %q about an ordinary process", got)
				}
				return
			}
			if len(got) != 1 || !strings.Contains(got[0], c.want) || !strings.Contains(got[0], "kill 5000") {
				t.Fatalf("manager heard %q, want once %q with how to end it", got, c.want)
			}
		})
	}
}
