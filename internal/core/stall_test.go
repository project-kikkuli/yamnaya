package core

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestAHungAgentReachesItsManagerOnceAndStopsCountingAsMovement(t *testing.T) {
	for _, c := range []struct {
		name     string
		doing    time.Duration
		crew     time.Duration
		wantHung bool
	}{
		{"last tool call long ago", -20 * time.Minute, 0, true},
		{"a long tool call", -9 * time.Minute, 0, false},
		{"a subagent still working", -40 * time.Minute, -2 * time.Minute, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "make the importer handle CSV", "/repo/august")
			w.spawn("agent", m.Session)
			w.clock = w.clock.Add(time.Hour)
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
	w := newOrgWorld(t)
	_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
	w.core.Signal = func(pid int, _ syscall.Signal) error {
		w.stop(w.claude.sessions[pid].ID)
		return nil
	}
	w.clock = w.clock.Add(time.Hour)
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
	if again.Session == m.Session || len(w.launches.starts) != 2 {
		t.Fatalf("hung manager kept session %s after %d launches, want a relaunch", again.Session, len(w.launches.starts))
	}
	if !strings.Contains(w.launches.starts[1].Prompt, "relaunched") {
		t.Fatalf("relaunch prompt lacks the resume note:\n%s", w.launches.starts[1].Prompt)
	}
}
