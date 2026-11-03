package core

import (
	"strings"
	"testing"
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
		{"harness knows setup is done", "SETUP theme", false, Idle, "", false, false},
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
	w := newOrgWorld(t)
	_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
	w.spawn("agent", m.Session)
	w.set("agent", func(s *Session) { s.CheckScreen = true })
	for _, p := range w.procs {
		if w.claude.sessions[p.PID].ID == "agent" {
			w.tmux.ttys[p.TTY] = "%9"
		}
	}
	w.tmux.screens["%9"] = "SETUP security notes"
	w.loop(t, 3)
	var heard []string
	for _, text := range w.handedTo(m.Session) {
		if strings.Contains(text, "onboarding") {
			heard = append(heard, text)
		}
	}
	if len(heard) != 1 || !strings.Contains(heard[0], "security notes") {
		t.Fatalf("manager heard %q; want the step once", heard)
	}
}
