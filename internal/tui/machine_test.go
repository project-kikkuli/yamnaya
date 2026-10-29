package tui

import (
	"strings"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/pressure"
	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func TestTheHeaderShowsSlotHoldersWaitersAndAnOverload(t *testing.T) {
	now := time.Now()
	use := func(goal, name string, cmd ...string) core.SlotUse {
		return core.SlotUse{Claim: slot.Claim{Slot: 1, Command: cmd, Since: now.Add(-3 * time.Minute)}, Goal: goal, Name: name}
	}
	for _, c := range []struct {
		name string
		m    core.Machine
		want []string
	}{
		{"no pool", core.Machine{}, nil},
		{"throttled", core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12, Pressure: pressure.State{Active: true, Holding: true}}, Load: 24},
			[]string{" slots 0/4 held · load 24.0 on 12 cores", " throttle: heavy agent jobs nice 15 (0 live), 0 slots held, next slot granted", " overloaded: load 24 on 12 cores"}},
		{"throttled with a slot held", core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12, Pressure: pressure.State{Active: true, Holding: true}}, Load: 24,
			Holders: []core.SlotUse{use("goal1", "PR tests", "go", "test", "./...")}},
			[]string{" slots 1/4 held · load 24.0 on 12 cores", " throttle: heavy agent jobs nice 15 (0 live), 1 slots held, new slots wait",
				"   slot 1  goal1 PR tests  go test ./...  3m", " overloaded: load 24 on 12 cores"}},
		{"recovering", core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12, Pressure: pressure.State{Active: true}}, Load: 10},
			[]string{" slots 0/4 held · load 10.0 on 12 cores", " throttle: recovering, new slots open; clears after 3 minutes without agent pressure"}},
		{"quiet", core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12}, Load: 3},
			[]string{" slots 0/4 held · load 3.0 on 12 cores"}},
		{"busy and overloaded", core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12}, Load: 140,
			Holders: []core.SlotUse{use("goal1", "PR tests", "go", "test", "./...")},
			Waiters: []core.SlotUse{use("goal3", "board", "make")},
			Hogs:    []core.Hog{{Goal: "goal1", Name: "PR tests", CPU: 620}}},
			[]string{" slots 1/4 held, 1 waiting · load 140.0 on 12 cores", "   slot 1  goal1 PR tests  go test ./...  3m",
				"   waiting  goal3 board  make  3m", " overloaded: load 140 on 12 cores · top: goal1 PR tests 620%"}},
	} {
		got := strings.Split(ansi.Strip(machineLines(c.m, now, 200)), "\n")[1:]
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s: header lines\n%q\nwant\n%q", c.name, got, c.want)
		}
	}
}

func TestThrottleTransitionsRefreshTheHeaderEvenWhenSessionsAreUnchanged(t *testing.T) {
	now := time.Now()
	m := Model{core: &core.Core{}, board: missionBoard(now), loaded: true, width: 200, height: 50, cursor: -1}
	m.board.Gen = 7
	m.board.Machine = core.Machine{Slots: slot.Status{Capacity: 4, Cores: 12}, Load: 10}
	page := m.prepareView()
	m.page = &page
	for _, state := range []pressure.State{{Active: true, Holding: true}, {Active: true}, {}} {
		board := m.board
		board.Machine.Slots.Pressure = state
		next, _ := m.Update(boardMsg{board: board})
		m = next.(Model)
		view := ansi.Strip(m.View().Content)
		if want := state.Line(len(m.board.Machine.Holders)); want != "" && !strings.Contains(view, want) {
			t.Fatalf("missing %q in %s", want, view)
		}
		if !state.Active && strings.Contains(view, "throttle:") {
			t.Fatal("throttle header did not clear", view)
		}
	}
}

func TestQTogglesTheBlanketThrottleAndTheBoardSaysSoWhileItIsOn(t *testing.T) {
	paused := pressure.Throttle{Since: time.Now(), Paused: []pressure.Paused{{PID: 4242, Started: 1, Command: "claude", Session: "s1"}}}
	for _, c := range []struct {
		name   string
		before pressure.Throttle
		said   string
	}{
		{"off pauses", pressure.Throttle{}, "paused 0 agents"},
		{"on resumes", paused, "resumed 1 processes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			m := emptyBoard(t)
			dir := t.TempDir()
			if err := c.before.Save(dir); err != nil {
				t.Fatal(err)
			}
			m.core.ThrottleDir = func() (string, error) { return dir, nil }
			m.core.Processes = func() ([]pressure.Process, error) { return nil, nil }
			m.core.ThrottleSignal = func(pressure.Process, syscall.Signal) error { return nil }
			m.board.Machine.Slots.Throttle = c.before
			if banner := ansi.Strip(machineAlerts(m.board.Machine, 200)); strings.Contains(banner, "THROTTLED") != c.before.On() {
				t.Fatalf("header %q while throttle on=%v", banner, c.before.On())
			}
			next, cmd := m.Update(keyOf("Q"))
			m = next.(Model)
			next, _ = m.Update(done(t, cmd))
			m = next.(Model)
			if !strings.Contains(m.message, c.said) {
				t.Fatalf("Q said %q, want %q", m.message, c.said)
			}
			after, err := pressure.ReadThrottle(dir)
			if err != nil || after.On() == c.before.On() {
				t.Fatalf("throttle after Q: %+v, %v", after, err)
			}
		})
	}
}

// done is the doneMsg one of cmd's batched commands produces; the others, such as animation ticks, are left running.
func done(t *testing.T, cmd tea.Cmd) doneMsg {
	t.Helper()
	found := make(chan doneMsg, 1)
	var start func(tea.Cmd)
	start = func(c tea.Cmd) {
		go func() {
			switch msg := c().(type) {
			case tea.BatchMsg:
				for _, c := range msg {
					if c != nil {
						start(c)
					}
				}
			case doneMsg:
				found <- msg
			}
		}()
	}
	start(cmd)
	select {
	case msg := <-found:
		return msg
	case <-time.After(10 * time.Second):
		t.Fatal("no doneMsg")
	}
	return doneMsg{}
}
