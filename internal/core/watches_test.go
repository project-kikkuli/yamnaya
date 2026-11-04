package core

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/slot"
)

func (w *orgWorld) nudged(session string) int {
	n := 0
	for _, text := range w.handedTo(session) {
		if strings.Contains(text, "nothing has moved") {
			n++
		}
	}
	return n
}

// claimSlot has session hold, or wait behind a holder for, the world's only slot until the returned func runs.
func (w *orgWorld) claimSlot(t *testing.T, session string, waiting bool) func() {
	t.Helper()
	pool := slot.Pool{Dir: t.TempDir(), Capacity: 1, Cores: 4}
	w.core.Slots = func() (slot.Pool, error) { return pool, nil }
	holder := session
	if waiting {
		holder = "someone-else"
	}
	release, _, err := pool.Acquire(context.Background(), slot.Claim{PID: os.Getpid(), Session: holder}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !waiting {
		return release
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	queued := make(chan struct{})
	go func() {
		defer close(done)
		pool.Acquire(ctx, slot.Claim{PID: os.Getpid(), Session: session}, func() { close(queued) })
	}()
	<-queued
	stop := func() {
		cancel()
		<-done
		release()
	}
	t.Cleanup(func() { cancel(); <-done; release() })
	return stop
}

func TestTheQuietWatchEndsInAFlagUnlessTheGoalCorrectlyWaits(t *testing.T) {
	for _, tc := range []struct {
		name      string
		arrange   func(t *testing.T, w *orgWorld, g Goal, m Manager)
		cycle     func(t *testing.T, w *orgWorld, g Goal, m Manager)
		reply     func(t *testing.T, w *orgWorld, g Goal, m Manager)
		wantNudge int
		wantStuck bool
	}{
		{name: "a manager answering each nudge with a brief and a reply", wantNudge: Nudges, wantStuck: true,
			reply: func(t *testing.T, w *orgWorld, g Goal, m Manager) {
				w.set(m.Session, func(s *Session) { s.Status, s.Since, s.ResultAt, s.Result = Idle, w.clock, w.clock, "No change." })
				if err := os.WriteFile(m.BriefPath, []byte("no change"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Chtimes(m.BriefPath, w.clock, w.clock); err != nil {
					t.Fatal(err)
				}
			}},
		{name: "a wake set", arrange: func(t *testing.T, w *orgWorld, g Goal, m Manager) {
			if _, err := w.core.SetWake(m.Session, w.clock.Add(24*time.Hour), "the hold"); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "an ask with a chief", arrange: func(t *testing.T, w *orgWorld, g Goal, m Manager) {
			w.chief(t, "boss", m.ID)
			w.ask(t, m.Session, "may I cut scope?")
		}},
		{name: "an ask with the owner", arrange: func(t *testing.T, w *orgWorld, g Goal, m Manager) {
			w.ask(t, m.Session, "may I merge in august?")
		}},
		{name: "an agent waiting for a slot", arrange: func(t *testing.T, w *orgWorld, g Goal, m Manager) {
			w.claimSlot(t, "agent", true)
		}},
		{name: "pull request news", cycle: func(t *testing.T, w *orgWorld, g Goal, m Manager) {
			if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIGreen, "CI passed", ""); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "the manager's goal update", cycle: func(t *testing.T, w *orgWorld, g Goal, m Manager) {
			if _, err := w.core.UpdateGoal(m.Session, "landed the first half"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			if tc.arrange != nil {
				tc.arrange(t, w, g, m)
			}
			for range 6 {
				w.clock = w.clock.Add(QuietAfter + time.Minute)
				if tc.cycle != nil {
					tc.cycle(t, w, g, m)
				}
				w.supervise(t)
				if tc.reply != nil {
					tc.reply(t, w, g, m)
					w.clock = w.clock.Add(2 * time.Minute)
					w.supervise(t)
				}
			}
			if got := w.nudged(m.Session); got != tc.wantNudge {
				t.Fatalf("nudged %d times, want %d", got, tc.wantNudge)
			}
			got, _ := w.core.Store.Goal(g.ID)
			if (got.State == GoalStuck) != tc.wantStuck {
				t.Fatalf("goal %s (%s), want stuck %v", got.State, got.Evidence, tc.wantStuck)
			}
		})
	}
}

func TestANudgedManagersOwnTurnCountsOnlyPastAReply(t *testing.T) {
	for _, c := range []struct {
		name      string
		running   time.Duration
		wantReset bool
	}{
		{"a short reply", time.Minute, false},
		{"a long turn of its own", nudgeReply + time.Minute, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			w.clock = w.clock.Add(QuietAfter + time.Minute)
			w.supervise(t)
			if w.nudged(m.Session) != 1 {
				t.Fatalf("heard %q, want one nudge", w.handedTo(m.Session))
			}
			w.clock = w.clock.Add(c.running)
			w.set(m.Session, func(s *Session) {
				s.Status, s.Since = Busy, w.clock.Add(-c.running)
				s.Doing = Action{Tool: "Bash", At: w.clock}
			})
			w.supervise(t)
			got, err := w.core.Store.Manager(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (got.Nudges == 0) != c.wantReset {
				t.Fatalf("nudges %d, want reset %v", got.Nudges, c.wantReset)
			}
		})
	}
}

func TestAWaitForASlotIsWorkInFlightAndNotAHang(t *testing.T) {
	for _, waiting := range []bool{false, true} {
		name := "holding"
		if waiting {
			name = "waiting"
		}
		t.Run(name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			w.clock = w.clock.Add(time.Hour)
			w.set("agent", func(s *Session) {
				s.Status, s.Since = Busy, w.clock.Add(-40*time.Minute)
				s.Doing = Action{Tool: "Bash", Detail: "ym slot -- go test ./...", At: w.clock.Add(-40 * time.Minute)}
			})
			w.set(m.Session, func(s *Session) { s.ResultAt, s.Result = w.clock.Add(-time.Minute), "Waiting on the agent." })
			release := w.claimSlot(t, "agent", waiting)
			w.loop(t, 2)
			kinds := w.kinds(t, g.ID)
			if slices.Contains(kinds, EventAgentHung) || slices.Contains(kinds, EventNudge) {
				t.Fatalf("events %q: a slot wait is neither hung nor nothing in flight", kinds)
			}
			release()
			w.loop(t, 1)
			if kinds := w.kinds(t, g.ID); slices.Contains(kinds, EventAgentHung) {
				t.Fatalf("events %q: the agent gets time to act once its slot ends", kinds)
			}
			w.clock = w.clock.Add(HungAfter + time.Minute)
			w.loop(t, 1)
			if kinds := w.kinds(t, g.ID); !slices.Contains(kinds, EventAgentHung) {
				t.Fatalf("events %q: silence long after the slot ended is a hang", kinds)
			}
		})
	}
}

func TestAUsageLimitIsItsOwnState(t *testing.T) {
	const line = "You've hit your weekly limit · resets Nov 5, 3am (UTC)"
	stop := func(w *orgWorld, id string) {
		w.set(id, func(s *Session) { s.Status, s.QuotaAt, s.Result, s.ResultAt = Idle, w.clock, line, w.clock })
	}
	countKind := func(t *testing.T, w *orgWorld, g Goal, kind string) int {
		t.Helper()
		n := 0
		for _, k := range w.kinds(t, g.ID) {
			if k == kind {
				n++
			}
		}
		return n
	}
	t.Run("an agent wakes its manager once and hears nothing", func(t *testing.T) {
		w := newOrgWorld(t)
		g, m := w.goal(t, "ship the importer", "/repo/august")
		w.spawn("agent", m.Session)
		stop(w, "agent")
		w.loop(t, 3)
		if n := countKind(t, w, g, EventAgentLimited); n != 1 {
			t.Fatalf("%d limit events, want 1: %q", n, w.kinds(t, g.ID))
		}
		if n := countKind(t, w, g, EventFinished) + countKind(t, w, g, EventIdleAgain); n != 0 {
			t.Fatalf("the limit line was recorded as a reply: %q", w.kinds(t, g.ID))
		}
		var wakes []string
		for _, text := range w.handedTo(m.Session) {
			if strings.Contains(text, EventAgentLimited) {
				wakes = append(wakes, text)
			}
		}
		if len(wakes) != 1 || !strings.Contains(wakes[0], "resets Nov 5") {
			t.Fatalf("manager heard %q, want one wake saying when it resets", wakes)
		}
		if err := w.core.tell(w.board(t), "agent", "ym: keep going", false); err != nil {
			t.Fatal(err)
		}
		w.loop(t, 1)
		if got := w.handedTo("agent"); len(got) != 0 {
			t.Fatalf("typed into a stopped session: %q", got)
		}
	})
	t.Run("a stopped goal gets no nudge, no flag and no notification", func(t *testing.T) {
		w := newOrgWorld(t)
		var shown []string
		w.core.Notify = func(title, body string) error { shown = append(shown, title); return nil }
		g, m := w.goal(t, "ship the importer", "/repo/august")
		w.spawn("agent", m.Session)
		w.clock = w.clock.Add(time.Hour)
		stop(w, m.Session)
		stop(w, "agent")
		for range 5 {
			w.clock = w.clock.Add(limitHold / 5)
			w.clock = w.clock.Add(-time.Minute)
			w.loop(t, 1)
		}
		if got := w.nudged(m.Session); got != 0 {
			t.Fatalf("nudged %d times", got)
		}
		got, _ := w.core.Store.Goal(g.ID)
		if got.State == GoalStuck || len(shown) != 0 {
			t.Fatalf("goal %s, owner shown %q: a limit is not stuck", got.State, shown)
		}
	})
	t.Run("a stopped manager is held until its limit resets", func(t *testing.T) {
		w := newOrgWorld(t)
		g, m := w.goal(t, "ship the importer", "/repo/august")
		w.clock = w.clock.Add(time.Hour)
		stop(w, m.Session)
		for range 4 {
			w.clock = w.clock.Add(limitHold/4 - time.Minute)
			w.loop(t, 1)
		}
		if got := w.handedTo(m.Session); len(got) != 0 {
			t.Fatalf("typed into a stopped manager: %q", got)
		}
		w.clock = time.Date(2026, 11, 5, 3, 5, 0, 0, time.UTC)
		w.loop(t, 1)
		var wakes int
		for _, text := range w.handedTo(m.Session) {
			if strings.Contains(text, EventAgentLimited) {
				wakes++
			}
		}
		if wakes != 1 {
			t.Fatalf("after its limit the manager heard %q, want one wake for the stop", w.handedTo(m.Session))
		}
		if n := countKind(t, w, g, EventStuck); n != 0 {
			t.Fatalf("%d stuck events", n)
		}
	})
	t.Run("what was queued during a long stop is typed after it", func(t *testing.T) {
		for _, c := range []struct {
			name      string
			stopped   time.Duration
			age       time.Duration
			wantTyped bool
		}{
			{"still stopped", -time.Minute, 30 * time.Minute, false},
			{"queued during a stop that has ended", -time.Minute, 2 * time.Hour, true},
			{"queued before the stop and now stale", time.Minute, 2 * time.Hour, false},
		} {
			t.Run(c.name, func(t *testing.T) {
				w := newWorld(t)
				w.run(20, 1, "/dev/t2", "claude", "s", Idle)
				w.tmux.ttys["/dev/t2"] = "%2"
				w.tmux.screens["%2"] = "> "
				stoppedAt := time.Now().Add(c.stopped)
				w.set("s", func(s *Session) { s.QuotaAt = stoppedAt })
				if _, err := w.core.Store.AddMessage("s", "carry on", MsgPending); err != nil {
					t.Fatal(err)
				}
				at := time.Now().Add(c.age)
				w.core.Now = func() time.Time { return at }
				if _, err := w.core.Redeliver(w.board(t)); err != nil {
					t.Fatal(err)
				}
				if got := len(w.tmux.typed) == 1; got != c.wantTyped {
					t.Fatalf("typed %q, want typed %v", w.tmux.typed, c.wantTyped)
				}
			})
		}
	})
	t.Run("a reset nobody can read holds an hour, a full window until it resets", func(t *testing.T) {
		now := time.Date(2026, 11, 3, 18, 0, 0, 0, time.UTC)
		c := &Core{Now: func() time.Time { return now }}
		s := Live{Session: Session{Harness: "claude", Account: "acct", QuotaAt: now.Add(-2 * time.Hour)}}
		window := Limit{Harness: "claude", Account: "acct", Name: "7d", Used: 100, ResetsAt: now.Add(time.Hour), Seen: s.QuotaAt.Add(-12 * time.Hour)}
		said := func(line string) Live {
			return Live{Session: Session{QuotaAt: s.QuotaAt, Facts: Facts{Result: line, ResultAt: s.QuotaAt}}}
		}
		for _, tc := range []struct {
			name   string
			s      Live
			limits []Limit
			want   bool
		}{
			{"no stop", Live{}, []Limit{window}, false},
			{"just stopped, no window read yet", Live{Session: Session{QuotaAt: now.Add(-time.Minute)}}, nil, true},
			{"past the hold with no window", s, nil, false},
			{"past the hold with a full window", s, []Limit{window}, true},
			{"another account's window", Live{Session: Session{Harness: "claude", Account: "other", QuotaAt: s.QuotaAt}}, []Limit{window}, false},
			{"its line names a later day", said("You've hit your weekly limit · resets Nov 5, 3am (UTC)"), nil, true},
			{"its line names a later hour", said("You've hit your session limit · resets 9:20pm (UTC)"), nil, true},
			{"its line names a reset already past", said("You've hit your session limit · resets 5pm (UTC)"), nil, false},
			{"its line names a reset inside the hold", Live{Session: Session{QuotaAt: now.Add(-30 * time.Minute), Facts: Facts{Result: "You've hit your session limit · resets 5:40pm (UTC)", ResultAt: now.Add(-30 * time.Minute)}}}, nil, false},
			{"an earlier reply is not its line", Live{Session: Session{QuotaAt: s.QuotaAt, Facts: Facts{Result: "resets Nov 5, 3am (UTC)", ResultAt: s.QuotaAt.Add(-time.Minute)}}}, nil, false},
		} {
			if got := c.limited(Board{Fleet: Fleet{Limits: tc.limits}}, tc.s); got != tc.want {
				t.Errorf("%s: limited = %v, want %v", tc.name, got, tc.want)
			}
		}
		if busy := (Live{Session: Session{Status: Busy, QuotaAt: now.Add(-time.Hour)}}); busy.Hung(now, time.Time{}) {
			t.Error("a session a limit stopped is not hung")
		}
	})
}

func TestARelayDestinationThatStopsPullingWakesWhoWaitsAndTellsTheOwner(t *testing.T) {
	const dest = "laptop"
	send := func(t *testing.T, w *orgWorld, requester string) {
		t.Helper()
		if err := w.core.Relay(dest, RelayYM, relayYM{Args: []string{"goal", "send", "--relayed", "mgr51", "hello"}, Requester: requester}); err != nil {
			t.Fatal(err)
		}
	}
	deliver := func(t *testing.T, w *orgWorld) {
		t.Helper()
		pending, err := w.core.Store.PendingRelays()
		if err != nil {
			t.Fatal(err)
		}
		var seqs []int64
		for _, r := range pending {
			if r.Dest == dest {
				seqs = append(seqs, r.Seq)
			}
		}
		if err := w.core.Store.markRelayed(seqs, w.clock); err != nil {
			t.Fatal(err)
		}
	}
	count := func(t *testing.T, w *orgWorld, g Goal, kind string) int {
		t.Helper()
		n := 0
		for _, k := range w.kinds(t, g.ID) {
			if k == kind {
				n++
			}
		}
		return n
	}
	relayItems := func(w *orgWorld) int {
		n := 0
		for _, it := range ownerItems(w.board(t)) {
			if strings.Contains(it.Question, dest) {
				n++
			}
		}
		return n
	}

	t.Run("an outage and its recovery", func(t *testing.T) {
		w := newOrgWorld(t)
		var shown []string
		w.core.Notify = func(title, body string) error { shown = append(shown, title); return nil }
		g, m := w.goal(t, "ship the importer", "/repo/august")
		other, n := w.goal(t, "ship the exporter", "/repo/august")
		bystander, _ := w.goal(t, "ship the docs", "/repo/august")
		send(t, w, m.Session)
		w.clock = w.clock.Add(RelayStallAfter - time.Minute)
		w.loop(t, 1)
		if count(t, w, g, EventRelayStalled) != 0 || relayItems(w) != 0 {
			t.Fatal("a relay waiting under the threshold is no stall")
		}
		w.clock = w.clock.Add(2 * time.Minute)
		w.loop(t, 3)
		if count(t, w, g, EventRelayStalled) != 1 || relayItems(w) != 1 || len(shown) != 1 {
			t.Fatalf("stalled events %d, owner items %d, notifications %q; want one each", count(t, w, g, EventRelayStalled), relayItems(w), shown)
		}
		if !slices.ContainsFunc(w.handedTo(m.Session), func(s string) bool { return strings.Contains(s, "relay to laptop stalled") }) {
			t.Fatalf("manager heard %q", w.handedTo(m.Session))
		}
		send(t, w, n.Session)
		w.loop(t, 2)
		if count(t, w, other, EventRelayStalled) != 1 {
			t.Fatal("a command queued during the outage wakes its manager at once")
		}
		if count(t, w, bystander, EventRelayStalled) != 0 {
			t.Fatal("a goal with nothing waiting hears nothing")
		}
		w.clock = w.clock.Add(time.Hour)
		w.loop(t, 2)
		if count(t, w, g, EventRelayStalled) != 1 || relayItems(w) != 1 || len(shown) != 1 {
			t.Fatal("an outage tells everyone once however long it lasts")
		}
		deliver(t, w)
		w.loop(t, 3)
		if count(t, w, g, EventRelayBack) != 1 || count(t, w, other, EventRelayBack) != 1 || relayItems(w) != 0 || len(shown) != 2 {
			t.Fatalf("recovery events %d and %d, owner items %d, notifications %q", count(t, w, g, EventRelayBack), count(t, w, other, EventRelayBack), relayItems(w), shown)
		}
		send(t, w, m.Session)
		w.clock = w.clock.Add(RelayStallAfter + time.Minute)
		w.loop(t, 1)
		if count(t, w, g, EventRelayStalled) != 2 || len(shown) != 3 {
			t.Fatalf("a second outage tells again: %d events, %q", count(t, w, g, EventRelayStalled), shown)
		}
	})

	t.Run("a refused relay shows the host still takes them", func(t *testing.T) {
		w := newOrgWorld(t)
		g, m := w.goal(t, "ship the importer", "/repo/august")
		send(t, w, m.Session)
		pending, _ := w.core.Store.PendingRelays()
		if _, err := w.core.Store.noteRefused(w.core.SelfName(), []RelayRef{{Origin: w.core.SelfName(), OriginSeq: pending[0].Seq, Error: "no such goal yet"}}, w.clock); err != nil {
			t.Fatal(err)
		}
		w.clock = w.clock.Add(time.Hour)
		w.loop(t, 1)
		if count(t, w, g, EventRelayStalled) != 0 || relayItems(w) != 0 {
			t.Fatal("a refusal is reported its own way, not as a stall")
		}
	})

	t.Run("the owner's notification does not wait on the stalled host", func(t *testing.T) {
		w := newOrgWorld(t)
		if err := w.core.Store.setRelaySelf(RelaySelf{Name: "vm", Courier: dest, At: w.clock}); err != nil {
			t.Fatal(err)
		}
		_, m := w.goal(t, "ship the importer", "/repo/august")
		send(t, w, m.Session)
		w.clock = w.clock.Add(RelayStallAfter + time.Minute)
		w.loop(t, 1)
		notifies := func() int {
			pending, _ := w.core.Store.PendingRelays()
			n := 0
			for _, r := range pending {
				if r.Kind == RelayNotify {
					n++
				}
			}
			return n
		}
		if notifies() != 0 || relayItems(w) != 1 {
			t.Fatalf("%d notifications queued on the stalled host, %d owner items", notifies(), relayItems(w))
		}
		deliver(t, w)
		w.loop(t, 1)
		if notifies() != 1 {
			t.Fatalf("%d notifications queued after recovery, want one saying it is back", notifies())
		}
	})
}
