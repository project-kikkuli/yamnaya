package core

import (
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func TestTheMachinePlacesSlotsAndHeavyProcessesWithTheirGoalAndSession(t *testing.T) {
	now := time.Now()
	b := Board{At: now,
		Sessions: []Live{{Session: Session{ID: "s-tests", PID: 100, Title: "tests"}}, {Session: Session{ID: "s-mgr", PID: 150}}},
		Tree: []Node{{Manager: Manager{ID: "mgr3", GoalID: "goal3"}, Goal: &Goal{ID: "goal3"},
			Live:    &Live{Session: Session{ID: "s-mgr", PID: 150}},
			Workers: []Worker{{Session: "s-tests", Name: "board tests", State: "busy"}}}},
	}
	procs := []proc.Proc{
		{PID: 100, PPID: 1, Args: []string{"claude"}},
		{PID: 150, PPID: 1, Args: []string{"claude"}},
		{PID: 200, PPID: 100, Args: []string{"ym", "slot", "--", "go", "test"}},
		{PID: 300, PPID: 200, Args: []string{"go", "test"}},
		{PID: 400, PPID: 1, Args: []string{"/usr/bin/WindowServer"}},
		{PID: 500, PPID: 1, Args: []string{"/usr/bin/sleepy"}},
	}
	c := &Core{
		Slots: func() (slot.Pool, error) { return slot.Pool{Dir: t.TempDir(), Capacity: 2, Cores: 4}, nil },
		Loads: func() float64 { return 9 },
		CPU: func() (map[int]float64, error) {
			return map[int]float64{300: 380, 100: 20, 400: 80, 500: 10, 150: 5}, nil
		},
	}
	c.machine(&b, procs)
	m := b.Machine
	if !m.Overloaded() {
		t.Fatalf("load 9 on 4 cores must count as overloaded")
	}
	if want := "overloaded: load 9 on 4 cores · top: goal3 board tests 400%, WindowServer 80%"; m.Overload() != want {
		t.Fatalf("Overload() = %q, want %q", m.Overload(), want)
	}
	u := placer(b, procs).use(slot.Claim{PID: 200, Command: []string{"go", "test"}, Since: now.Add(-4 * time.Minute)})
	if u.Session != "s-tests" || !strings.HasPrefix(u.Line(now), "goal3 board tests  go test  4m") {
		t.Fatalf("use = %+v, line %q", u, u.Line(now))
	}
	if u := placer(b, procs).use(slot.Claim{PID: 999, Session: "s-mgr"}); u.Who() != "goal3 mgr3" {
		t.Fatalf("a claim naming its session in the environment is placed by it; got %q", u.Who())
	}
}
