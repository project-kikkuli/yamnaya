package core

import (
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRefreshStepRestartsOnlyAnIdleSessionPastTheCapWithItsBriefCurrent(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	asked := now.Add(-time.Minute)
	for _, tc := range []struct {
		name           string
		tokens, limit  int64
		idle, stale    bool
		asked, replied time.Time
		want           refreshMove
	}{
		{"under the cap", 90_000, 100_000, true, false, timeZero, now, wakeAsUsual},
		{"cap off", 900_000, 0, true, false, timeZero, now, wakeAsUsual},
		{"past the cap mid-turn", 260_000, 100_000, false, false, timeZero, now, holdWake},
		{"past the cap, idle, brief current", 260_000, 100_000, true, false, timeZero, now, restartFresh},
		{"past the cap, idle, brief stale", 260_000, 100_000, true, true, timeZero, now, askBrief},
		{"asked for a brief, not yet replied", 260_000, 100_000, true, true, asked, asked.Add(-time.Hour), holdWake},
		{"asked for a brief, replied without one", 260_000, 100_000, true, true, asked, now, restartFresh},
		{"asked for a brief, wrote it", 260_000, 100_000, true, false, asked, now, restartFresh},
		{"asked long ago, never replied", 260_000, 100_000, true, true, now.Add(-BriefGrace), timeZero, restartFresh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := refreshStep(tc.tokens, tc.limit, tc.idle, tc.stale, tc.asked, tc.replied, now); got != tc.want {
				t.Fatalf("refreshStep = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResumeColdOnlyWhenTheTranscriptIsLargeAndItsCacheExpired(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		tokens int64
		at     time.Time
		want   bool
	}{
		{"large and cold", 240_000, now.Add(-2 * time.Hour), true},
		{"large and warm", 240_000, now.Add(-10 * time.Minute), false},
		{"small and cold", 40_000, now.Add(-2 * time.Hour), false},
		{"never seen", 240_000, timeZero, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resumeCold(tc.tokens, 100_000, tc.at, now); got != tc.want {
				t.Fatalf("resumeCold = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAManagerPastItsContextCapGetsItsWakeInAFreshSessionFromItsBrief(t *testing.T) {
	for _, tc := range []struct {
		name      string
		tokens    int64
		busy      bool
		brief     bool
		restarted bool
		woken     bool
		askedOld  bool
	}{
		{name: "under the cap", tokens: 40_000, brief: true, woken: true},
		{name: "past the cap with its brief current", tokens: 120_000, brief: true, restarted: true},
		{name: "past the cap mid-turn", tokens: 120_000, brief: true, busy: true},
		{name: "past the cap with no brief", tokens: 120_000, askedOld: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Signal = func(pid int, _ syscall.Signal) error {
				w.stop(w.claude.sessions[pid].ID)
				return nil
			}
			if err := w.core.Store.SetContextCap(100_000); err != nil {
				t.Fatal(err)
			}
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent-1", m.Session)
			w.set("agent-1", func(s *Session) { s.Title, s.Result, s.ResultAt = "parser agent", "Pushed the CSV parser.", w.clock })
			if _, err := w.core.SteerContext(w.board(t), m.ID, "ship CSV first, TSV later", Owner); err != nil {
				t.Fatal(err)
			}
			if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, "CI failed on the parser: 1 failing", "red-1"); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Minute)
			if tc.brief {
				if _, err := w.core.WriteBrief(m.Session, Brief{Assessment: "parser half done", Changed: "CI red", Uncertainty: "none", Next: "fix the test", WaitingOn: "CI"}); err != nil {
					t.Fatal(err)
				}
			}
			w.set(m.Session, func(s *Session) {
				s.Context, s.ContextAt, s.Result, s.ResultAt = tc.tokens, w.clock, "Looked at CI.", w.clock
				if tc.busy {
					s.Status = Busy
				}
			})
			w.loop(t, 1)
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			now := o.Managers[m.ID]
			if restarted := now.Session != m.Session; restarted != tc.restarted {
				t.Fatalf("manager went %s -> %s, want restarted=%v", Short(m.Session), Short(now.Session), tc.restarted)
			}
			old := strings.Join(w.handedTo(m.Session), "\n")
			if woken := strings.Contains(old, "CI failed on the parser"); woken != tc.woken {
				t.Fatalf("old session woken=%v, want %v:\n%s", woken, tc.woken, old)
			}
			if asked := strings.Contains(old, "Record a fresh brief now"); asked != tc.askedOld {
				t.Fatalf("old session asked for a brief=%v, want %v:\n%s", asked, tc.askedOld, old)
			}
			if !tc.restarted {
				return
			}
			prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
			for _, want := range []string{"ym restarted you fresh", "120k tokens", "parser half done", "ship CSV first, TSV later", "CI failed on the parser", "parser agent", "Pushed the CSV parser."} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("fresh session's prompt lacks %q:\n%s", want, prompt)
				}
			}
			if !w.board(t).Alive(now) {
				t.Fatal("the fresh session is not running")
			}
			if _, ok := w.board(t).Session(m.Session); ok {
				t.Fatal("the old session still runs")
			}
			events, _ := w.core.Store.GoalEvents(g.ID)
			if last := events[len(events)-1]; last.Kind != EventRefreshed || !strings.HasPrefix(last.Payload, "refreshed: 120k -> fresh") {
				t.Fatalf("last event is %s %q, want the refresh", last.Kind, last.Payload)
			}
			w.clock = w.clock.Add(time.Minute)
			w.loop(t, 2)
			if got := wakes(w.handedTo(now.Session)); len(got) != 0 {
				t.Fatalf("the fresh session was woken again with what its prompt carried: %q", got)
			}
		})
	}
}

func TestAManagerAskedForABriefIsRestartedOnceItEndsThatTurn(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Signal = func(pid int, _ syscall.Signal) error {
		w.stop(w.claude.sessions[pid].ID)
		return nil
	}
	g, m := w.goal(t, "ship the importer", "/repo/august")
	if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, "CI failed on the parser: 1 failing", "red-1"); err != nil {
		t.Fatal(err)
	}
	w.set(m.Session, func(s *Session) { s.Context, s.ContextAt, s.Result, s.ResultAt = 150_000, w.clock, "Looked.", w.clock })
	w.loop(t, 2)
	if n := strings.Count(strings.Join(w.handedTo(m.Session), "\n"), "Record a fresh brief now"); n != 1 {
		t.Fatalf("old session was asked for a brief %d times, want once", n)
	}
	w.clock = w.clock.Add(time.Minute)
	if _, err := w.core.WriteBrief(m.Session, Brief{Assessment: "the parser test is flaky, not wrong", Changed: "x", Uncertainty: "none", Next: "retry", WaitingOn: "CI"}); err != nil {
		t.Fatal(err)
	}
	w.set(m.Session, func(s *Session) { s.ResultAt = w.clock })
	w.loop(t, 1)
	o, _ := w.core.LoadOrg()
	if o.Managers[m.ID].Session == m.Session {
		t.Fatal("the manager was not restarted after it wrote its brief")
	}
	if prompt := w.launches.starts[len(w.launches.starts)-1].Prompt; !strings.Contains(prompt, "the parser test is flaky, not wrong") || !strings.Contains(prompt, "CI failed on the parser") {
		t.Fatalf("fresh session lacks the new brief or the wake:\n%s", prompt)
	}
}

func TestADownManagerWhoseResumeWouldBeColdAndLargeStartsFreshFromItsBrief(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tokens  int64
		idle    time.Duration
		resumed bool
	}{
		{"large and cold", 200_000, 2 * time.Hour, false},
		{"large and warm", 200_000, 10 * time.Minute, true},
		{"small and cold", 30_000, 2 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			r.state(m.Session, "running")
			w.set(m.Session, func(s *Session) { s.Context, s.ContextAt = tc.tokens, w.clock })
			w.loop(t, 1)
			w.clock = w.clock.Add(tc.idle)
			w.restart(r)
			w.loop(t, 1)
			o, _ := w.core.LoadOrg()
			now := o.Managers[m.ID]
			if resumed := len(r.resumed) == 1 && now.Session == m.Session; resumed != tc.resumed {
				t.Fatalf("resumed %+v, session %s -> %s; want resumed=%v", r.resumed, Short(m.Session), Short(now.Session), tc.resumed)
			}
			if tc.resumed {
				return
			}
			if prompt := w.launches.starts[len(w.launches.starts)-1].Prompt; !strings.Contains(prompt, "its prompt cache had expired") {
				t.Fatalf("fresh session does not say why it started over:\n%s", prompt)
			}
			events, _ := w.core.Store.GoalEvents(g.ID)
			if last := events[len(events)-1]; !strings.HasPrefix(last.Payload, "refreshed: 200k cold -> fresh") {
				t.Fatalf("last event is %q, want the cold refresh", last.Payload)
			}
		})
	}
}
