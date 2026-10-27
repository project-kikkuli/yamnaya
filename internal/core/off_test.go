package core

import (
	"strings"
	"testing"
	"time"
)

func (w *orgWorld) got(session string) []string {
	out := w.handedTo(session)
	for _, s := range w.launches.sent {
		if text, ok := strings.CutPrefix(s, session+": "); ok {
			out = append(out, text)
		}
	}
	return out
}

func TestYmOffHoldsEveryDeliveryAndOnHandsItOverInOneMessage(t *testing.T) {
	type deliver func(t *testing.T, w *orgWorld, g Goal, m Manager, agent string)
	for _, d := range []struct {
		name    string
		toAgent bool
		// suspended is supervision, which ym does not hold while off but takes up again once ym is on.
		suspended bool
		want      string
		do        deliver
	}{
		{"a wake with the goal's events", false, false, "CI failed on pr 7", func(t *testing.T, w *orgWorld, g Goal, m Manager, agent string) {
			if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, "CI failed on pr 7", ""); err != nil {
				t.Fatal(err)
			}
		}},
		{"an answer to the manager's ask", false, false, "ship it", func(t *testing.T, w *orgWorld, g Goal, m Manager, agent string) {
			it := w.ask(t, m.Session, "ready?")
			if _, _, err := w.core.Answer(w.board(t), it.Key(), "ship it"); err != nil {
				t.Fatal(err)
			}
		}},
		{"an answer to the agent's ask", true, false, "use csv", func(t *testing.T, w *orgWorld, g Goal, m Manager, agent string) {
			it := w.ask(t, agent, "which format?")
			if _, _, err := w.core.Answer(w.board(t), it.Key(), "use csv"); err != nil {
				t.Fatal(err)
			}
		}},
		{"the owner's steer", false, false, "focus on csv", func(t *testing.T, w *orgWorld, g Goal, m Manager, agent string) {
			if _, err := w.core.Steer(w.board(t), []string{m.ID}, false, true, "focus on csv"); err != nil {
				t.Fatal(err)
			}
		}},
		{"a message to the agent", true, false, "rebase first", func(t *testing.T, w *orgWorld, g Goal, m Manager, agent string) {
			if err := w.core.tell(w.board(t), agent, "rebase first", false); err != nil {
				t.Fatal(err)
			}
		}},
		{"a quiet nudge", false, true, "nothing has moved on goal", func(t *testing.T, w *orgWorld, g Goal, m Manager, agent string) {
			w.clock = w.clock.Add(2 * QuietAfter)
		}},
	} {
		for _, off := range []bool{false, true} {
			name := d.name + "/on"
			if off {
				name = d.name + "/off"
			}
			t.Run(name, func(t *testing.T) {
				w := newOrgWorld(t)
				g, m := w.goal(t, "make the importer handle CSV", "/repo/august")
				agent := "agent-1"
				w.spawn(agent, m.Session)
				w.loop(t, 1)
				w.supervise(t)
				w.claude.handed, w.launches.sent = nil, nil
				if off {
					mg := m
					if _, err := w.core.SetOff(w.board(t), OffTarget{Manager: &mg}, true); err != nil {
						t.Fatal(err)
					}
				}
				d.do(t, w, g, m, agent)
				w.loop(t, 2)
				if _, err := w.core.Redeliver(w.board(t)); err != nil {
					t.Fatal(err)
				}
				to, other := m.Session, agent
				if d.toAgent {
					to, other = agent, m.Session
				}
				if !off {
					if got := strings.Join(w.got(to), "\n"); !strings.Contains(got, d.want) {
						t.Fatalf("with ym on, %s got %q, want %q", Short(to), got, d.want)
					}
					return
				}
				if got := append(w.got(m.Session), w.got(agent)...); len(got) > 0 {
					t.Fatalf("with ym off, the goal's sessions got %q, want nothing", got)
				}
				b := w.board(t)
				n, _ := b.Node(m.ID)
				if !n.Off || n.Standing != StandOff {
					t.Fatalf("goal stands %q (off %v) while ym is off, want %q", n.Standing, n.Off, StandOff)
				}
				for _, key := range []string{m.Session, agent} {
					if s, _ := b.Session(key); !s.Off {
						t.Fatalf("session %s does not show ym off", Short(key))
					}
				}
				if rows := goalRows(b); len(rows) > 0 {
					t.Fatalf("an off goal shows in the owner's queue: %q", rows)
				}
				mg := m
				if _, err := w.core.SetOff(w.board(t), OffTarget{Manager: &mg}, false); err != nil {
					t.Fatal(err)
				}
				if d.suspended {
					w.supervise(t)
					if got := strings.Join(w.got(to), "\n"); !strings.Contains(got, d.want) {
						t.Fatalf("once ym is on, %s got %q, want supervision to resume with %q", Short(to), got, d.want)
					}
					return
				}
				got := w.got(to)
				if len(got) != 1 || !strings.Contains(got[0], "While it was off") || !strings.Contains(got[0], d.want) {
					t.Fatalf("after ym on, %s got %q, want one message holding %q", Short(to), got, d.want)
				}
				if others := strings.Join(w.got(other), "\n"); strings.Contains(others, d.want) {
					t.Fatalf("after ym on, %s got %q, meant for %s", Short(other), others, Short(to))
				}
				w.loop(t, 2)
				if _, err := w.core.Redeliver(w.board(t)); err != nil {
					t.Fatal(err)
				}
				for _, s := range append(w.got(to), w.got(other)...) {
					if strings.Contains(s, d.want) && s != got[0] {
						t.Fatalf("%q arrived again after ym on: %q", d.want, s)
					}
				}
			})
		}
	}
}

func TestYmOffCoversTheTreeItWasTurnedOffFor(t *testing.T) {
	for _, c := range []struct {
		name string
		off  func(g Goal, m, chief Manager) string
		// want lists, per session, whether ym is off for it.
		want map[string]bool
	}{
		{"a goal", func(g Goal, m, chief Manager) string { return m.ID }, map[string]bool{"manager": true, "agent": true, "subagent": true, "chief": false, "chief-agent": false}},
		{"a chief", func(g Goal, m, chief Manager) string { return chief.ID }, map[string]bool{"manager": false, "agent": false, "subagent": false, "chief": true, "chief-agent": true}},
		{"an agent", func(g Goal, m, chief Manager) string { return "agent" }, map[string]bool{"manager": false, "agent": true, "subagent": true, "chief": false, "chief-agent": false}},
		{"nothing", func(g Goal, m, chief Manager) string { return "" }, map[string]bool{"manager": false, "agent": false, "subagent": false, "chief": false, "chief-agent": false}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "make the importer handle CSV", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.supervise(t)
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			chief = o.Managers[chief.ID]
			w.spawn("agent", m.Session)
			w.spawn("subagent", "agent")
			w.spawn("chief-agent", chief.Session)
			if target := c.off(g, m, chief); target != "" {
				if err := w.core.Store.SetOff(target, true); err != nil {
					t.Fatal(err)
				}
			}
			names := map[string]string{"manager": m.Session, "agent": "agent", "subagent": "subagent", "chief": chief.Session, "chief-agent": "chief-agent"}
			b := w.board(t)
			for who, want := range c.want {
				if got := w.core.Off(names[who]); got != want {
					t.Errorf("%s: Off = %v, want %v", who, got, want)
				}
				if s, _ := b.Session(names[who]); s.Off != want {
					t.Errorf("%s: board shows off %v, want %v", who, s.Off, want)
				}
			}
		})
	}
}

func TestYmOffLeavesADeadManagerDown(t *testing.T) {
	for _, off := range []bool{false, true} {
		w := newOrgWorld(t)
		_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
		if off {
			if err := w.core.Store.SetOff(m.ID, true); err != nil {
				t.Fatal(err)
			}
		}
		w.stop(m.Session)
		starts := len(w.launches.starts)
		w.clock = w.clock.Add(StartGrace + time.Minute)
		w.supervise(t)
		if relaunched := len(w.launches.starts) > starts; relaunched == off {
			t.Fatalf("ym off %v: relaunched %v", off, relaunched)
		}
	}
}
