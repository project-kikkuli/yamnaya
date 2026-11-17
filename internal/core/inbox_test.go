package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestManagersReceiveOnlyConcreteDueActionsWhileStatusRemainsReadable(t *testing.T) {
	for _, tc := range []struct {
		name, kind, payload string
		due                 time.Duration
		action              bool
	}{
		{"failed CI", EventCIRed, "fix the failed check", WakeDebounce, true},
		{"machine overload stays observable", EventMachineThrottled, "load 76 on 12 cores; slots held", WakeDebounce, false},
		{"agent report", EventFinished, "agent a1 (work a1) is idle. Its last message, verbatim: PR is up.", DigestEvery, true},
		{"merge conflict", EventConflict, "resolve the base conflict", WakeDebounce, true},
		{"explicit timer", EventTimer, "check the canary error rate", WakeDebounce, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Pace = nil
			g, m := w.goal(t, "ship the importer", "/repo/august")
			start := w.clock
			payload := tc.payload
			if tc.kind == EventCIRed || tc.kind == EventConflict {
				url := "https://github.com/acme/august/pull/7"
				payload += " " + url
				st := PRStatus{State: "OPEN", Fetched: w.clock, Merge: PRMergeFacts{OID: "head"}}
				if tc.kind == EventCIRed {
					st.Failed = 1
				} else {
					st.Merge.Mergeable = "CONFLICTING"
				}
				if err := w.core.Store.SavePRStatus(url, st); err != nil {
					t.Fatal(err)
				}
			}
			for _, e := range []struct{ kind, payload string }{{tc.kind, payload}, {EventUpdate, "passive status"}, {EventIdleAgain, "still waiting"}} {
				if _, _, err := w.core.RecordGoalEvent(g.ID, e.kind, e.payload, ""); err != nil {
					t.Fatal(err)
				}
			}
			for _, at := range []time.Duration{0, tc.due - time.Second, tc.due, tc.due + time.Second} {
				w.clock = start.Add(at)
				if err := w.core.Wake(w.board(t)); err != nil {
					t.Fatal(err)
				}
				w.dispatch(t)
				got := wakes(w.handedTo(m.Session))
				want := 0
				if tc.action && at >= tc.due {
					want = 1
				}
				if len(got) != want {
					t.Fatalf("at %s got %q, want %d native submissions", at, got, want)
				}
				if want == 1 {
					if !strings.Contains(got[0], tc.kind+":") {
						t.Fatalf("action missing its source: %s", got[0])
					}
					for _, passive := range []string{"passive status", "still waiting"} {
						if strings.Contains(got[0], passive) {
							t.Fatalf("observation consumed prompt input: %s", got[0])
						}
					}
				}
			}
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil || len(events) != 3 {
				t.Fatalf("readable evidence lost: %+v %v", events, err)
			}
		})
	}
}

func TestAWakeCarriesEachEventInOneShortLineAndYmGoalEventPrintsItWhole(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.spawn("a1", m.Session)
	w.set("a1", func(s *Session) { s.Status = Busy; s.Since = w.clock.Add(-HungAfter - time.Minute) })
	long := "agent a1 (work a1) is idle. Its last message, verbatim: " + strings.Repeat("every detail of the parser, ", 200) + "THE END"
	e, _, err := w.core.RecordGoalEvent(g.ID, EventAgentHung, long, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := w.core.Wake(w.board(t)); err != nil {
		t.Fatal(err)
	}
	w.dispatch(t)
	got := wakes(w.handedTo(m.Session))
	if len(got) != 1 {
		t.Fatalf("manager woke %d times, want once", len(got))
	}
	for _, line := range strings.Split(got[0], "\n") {
		if n := len([]rune(line)); strings.Contains(line, "agent-hung:") && n > lineMax {
			t.Fatalf("a wake line runs %d characters, want at most about %d:\n%s", n, lineMax, line)
		}
	}
	if strings.Contains(got[0], "THE END") || !strings.Contains(got[0], "#"+strconv.FormatInt(e.Seq, 10)+" agent-hung: agent a1 (work a1): every detail") {
		t.Fatalf("wake does not name the event by number with the start of its text:\n%s", got[0])
	}
	whole, err := w.core.Store.GoalEvent(e.Seq)
	if err != nil || whole.Payload != long {
		t.Fatalf("event %d reads back %q, %v; want its whole text", e.Seq, whole.Payload, err)
	}
}

func TestAnAgentsReplyThatAddsNothingIsCountedNotDelivered(t *testing.T) {
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q")
	commit := func(name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(repo, name), []byte(name), 0o644); err != nil {
			t.Fatal(err)
		}
		git("add", name)
		git("commit", "-q", "-m", name)
	}
	commit("first")
	for _, tc := range []struct {
		name    string
		between func(t *testing.T, w *orgWorld)
		second  string
		news    bool
	}{
		{"a still-waiting note", nil, "Full suite still running (3 min in, niced). Push happens when it passes.", false},
		{"a repeat of the last reply", nil, "Parser is pushed as a1b2c3d and the PR is up for review.", false},
		{"a reply that says itself nothing is new", nil, "The implementer's idle message repeats its push report for `f750d8e`, so there's nothing new to act on. Its CI is green and I am waiting.", false},
		{"a reply that repeats but adds a fact", nil, "The implementer's idle message repeats its earlier report, but it adds one fact: the shard hung on a mutant.", true},
		{"a short answer that is not a wait", nil, "CSV only; TSV handling is gone.", true},
		{"a still-waiting note after a new commit", func(t *testing.T, w *orgWorld) { commit("second") }, "Full suite still running. Push happens when it passes.", true},
		{"a still-waiting note after an ask", func(t *testing.T, w *orgWorld) { w.ask(t, "agent", "may I skip the flaky test?") }, "Waiting on your answer.", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			reply := func(text string) {
				w.clock = w.clock.Add(time.Minute)
				w.set("agent", func(s *Session) { s.Worktree, s.Result, s.ResultAt = repo, text, w.clock })
				w.loop(t, 1)
			}
			reply("Parser is pushed as a1b2c3d and the PR is up for review.")
			w.dispatch(t)
			w.completeActions(t, m.Session)
			if tc.between != nil {
				tc.between(t, w)
			}
			before := len(w.handedTo(m.Session))
			reply(tc.second)
			w.dispatch(t)
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if got := last.Kind == EventFinished; got != tc.news || (!got && last.Kind != EventIdleAgain) {
				t.Fatalf("second reply recorded as %s, want news=%v", last.Kind, tc.news)
			}
			if heard := strings.Join(w.handedTo(m.Session)[before:], "\n"); strings.Contains(heard, tc.second) != tc.news {
				t.Fatalf("manager heard %q; want the second reply delivered=%v", heard, tc.news)
			}
		})
	}
}

func TestAChiefsOwnAgentsReachItInItsInboxNotVerbatim(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	c := w.chief(t, "platform", m.ID)
	w.loop(t, 1)
	chief, _ := w.core.Store.Manager(c.ID)
	w.spawn("helper", chief.Session)
	long := "Audit done. " + strings.Repeat("finding with evidence, ", 100)
	w.set("helper", func(s *Session) { s.Result, s.ResultAt = long, w.clock })
	w.loop(t, 2)
	w.dispatch(t)
	got := wakes(w.handedTo(chief.Session))
	if len(got) != 1 || !strings.Contains(got[0], "agent-finished:") || strings.Contains(got[0], long) {
		t.Fatalf("chief heard %q, want one wake with a line for its agent's reply, not the reply whole", got)
	}
}
