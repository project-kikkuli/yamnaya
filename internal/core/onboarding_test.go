package core

import (
	"strings"
	"testing"
	"time"
)

func TestAPaneOnFirstRunSetupReadsAsOnboarding(t *testing.T) {
	for _, c := range []struct {
		name        string
		screen      string
		checkScreen bool
		status      Status
		step        string
		prompt      bool
		need        bool
	}{
		{"setup on screen", "SETUP theme", true, Onboarding, "theme", false, true},
		{"input box on screen", "> ", true, Idle, "", false, false},
		{"permission prompt on screen", "DIALOG Bash", true, Idle, "", true, true},
		{"real permission prompt quoting setup text wins", "DIALOG SETUP Bash", true, Idle, "", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.run(100, 1, "/dev/ttys1", "claude", "s1", Idle)
			s := w.claude.sessions[100]
			s.CheckScreen = c.checkScreen
			w.claude.sessions[100] = s
			w.tmux.ttys["/dev/ttys1"] = "%1"
			w.tmux.screens["%1"] = c.screen

			b := w.board(t)
			got := b.Sessions[0]
			if got.Status != c.status || (got.Prompt != nil) != c.prompt {
				t.Fatalf("board: status %v, prompt %v; want %v, prompt %v", got.Status, got.Prompt, c.status, c.prompt)
			}
			if c.status == Onboarding && (got.WaitingFor != c.step || got.Status.String() != "onboarding") {
				t.Fatalf("board: state %q on step %q; want onboarding on %q", got.Status, got.WaitingFor, c.step)
			}
			if need := len(b.Needs) == 1 && b.Needs[0].Session != nil; need != c.need {
				t.Fatalf("needs %+v; want a row %v", b.Needs, c.need)
			}
			found, err := w.core.Find("s1")
			if err != nil {
				t.Fatal(err)
			}
			if settled := c.status == Idle && !c.prompt; found.Status != c.status || found.Settled() != settled {
				t.Fatalf("Find: status %v, settled %v; want %v, settled %v", found.Status, found.Settled(), c.status, settled)
			}
			if ParseStatus(got.Status.String()) != got.Status {
				t.Fatalf("%v does not survive a remote board", got.Status)
			}
		})
	}
}

func TestAManagerHearsOnceThatItsAgentSitsOnOnboarding(t *testing.T) {
	for _, c := range []struct {
		name string
		tick time.Duration
	}{
		{"ticks in the same instant", 0},
		{"ticks minutes apart", 5 * time.Minute},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "make the importer handle CSV", "/repo/august")
			w.spawn("agent", m.Session)
			w.set("agent", func(s *Session) { s.CheckScreen = true })
			for _, p := range w.procs {
				if w.claude.sessions[p.PID].ID == "agent" {
					w.tmux.ttys[p.TTY] = "%9"
				}
			}
			w.tmux.screens["%9"] = "SETUP security notes"
			for range 3 {
				w.loop(t, 1)
				w.clock = w.clock.Add(c.tick)
			}
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			var recorded []string
			for _, e := range events {
				if e.Kind == EventAgentOnboarding {
					recorded = append(recorded, e.Payload)
				}
			}
			if len(recorded) != 1 {
				t.Fatalf("recorded %q; want the step once", recorded)
			}
			var heard []string
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, "onboarding") {
					heard = append(heard, text)
				}
			}
			if len(heard) != 1 || !strings.Contains(heard[0], "security notes") {
				t.Fatalf("manager heard %q; want the step once", heard)
			}
		})
	}
}

func TestTheBoardReadsNoScreenOfASessionPastSetup(t *testing.T) {
	for _, st := range []Status{Busy, Idle} {
		t.Run(st.String(), func(t *testing.T) {
			w := newWorld(t)
			w.run(100, 1, "/dev/ttys1", "claude", "s1", st)
			w.tmux.ttys["/dev/ttys1"] = "%1"
			w.board(t)
			if w.tmux.reads != 0 {
				t.Fatalf("%d screen reads", w.tmux.reads)
			}
		})
	}
}

func TestOnboardingWaitsOnTheOwnerLikeADialog(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
	w.spawn("agent", m.Session)
	w.set("agent", func(s *Session) { s.CheckScreen, s.Since = true, w.clock.Add(-time.Hour) })
	for _, p := range w.procs {
		if w.claude.sessions[p.PID].ID == "agent" {
			w.tmux.ttys[p.TTY] = "%9"
		}
	}
	w.tmux.screens["%9"] = "SETUP theme"
	b := w.board(t)
	s, ok := b.Session("agent")
	if !ok || s.Status != Onboarding {
		t.Fatalf("agent %+v", s)
	}
	act := activity{sessions: map[string][]*Live{m.ID: {&s}}}
	if got := w.core.lastMoved(act, m); !got.Equal(w.core.now()) {
		t.Errorf("lastMoved = %v, want now: a goal waiting on the owner has not stalled", got)
	}
	if !w.core.inFlight(b, b.Org, m) {
		t.Error("inFlight false: its manager would be nudged though the owner holds the agent")
	}
	if !w.core.busyTold(s, w.clock.Add(-2*time.Hour)) {
		t.Error("busyTold false: a digest would be typed into the setup screen")
	}
}
