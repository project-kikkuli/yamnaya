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

func TestAManagerIsWokenOnlyForWhatNeedsItAMinuteAfterTheFirstAndTheRestComesAsADigest(t *testing.T) {
	type event struct {
		at          time.Duration
		kind, about string
	}
	type wake struct {
		at   time.Duration
		has  []string
		lack []string
	}
	for _, tc := range []struct {
		name   string
		events []event
		wakes  []wake
	}{
		{"an ask waits a minute for followers, then wakes once with all of them",
			[]event{{0, EventAsked, "C1 ask from agent a1: CSV or TSV?"}, {20 * time.Second, EventCIRed, "CI failed on #7"}, {40 * time.Second, EventPROpened, "agent a1 opened #8"}},
			[]wake{{time.Minute, []string{"2 for you, 1 more", "ci-red: CI failed on #7", "pr-opened: agent a1 opened #8", "asked: C1 ask from agent a1: CSV or TSV?"}, nil}}},
		{"overload tells its manager through the urgent path once",
			[]event{{0, EventMachineThrottled, "load 76 on 12 cores; child jobs nice 15; slots held; top: python3 pid 32727 78%"}},
			[]wake{{WakeDebounce, []string{"1 for you", "machine-throttled:", "python3 pid 32727 78%"}, nil}}},
		{"reports and green CI wait for the digest",
			[]event{{0, EventFinished, "agent a1 (work a1) is idle. Its last message, verbatim: PR is up."}, {5 * time.Minute, EventCIGreen, "CI passed on #7"}},
			[]wake{{DigestEvery, []string{"digest of 2", "agent-finished: agent a1 (work a1): PR is up.", "ci-green: CI passed on #7"}, nil}}},
		{"what waits rides along with the next wake",
			[]event{{0, EventMerged, "merged #6"}, {10 * time.Minute, EventConflict, "#7 conflicts with its base branch"}},
			[]wake{{10*time.Minute + WakeDebounce, []string{"1 for you, 1 more", "conflict: #7", "merged: merged #6"}, nil}}},
		{"replies that add nothing never wake anyone and are only counted",
			[]event{{0, EventIdleAgain, "agent a1 is idle. Its last message, verbatim: still waiting on the suite"}, {DigestEvery, EventTimer, "the wake you set"}},
			[]wake{{DigestEvery + WakeDebounce, []string{"1 for you:", "timer: the wake you set", "(1 agent reply with nothing new left out)"}, []string{"still waiting on the suite"}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Pace = nil
			g, m := w.goal(t, "ship the importer", "/repo/august")
			start := w.clock
			pending := tc.events
			check := map[time.Duration]wake{}
			var times []time.Duration
			for _, e := range tc.events {
				times = append(times, e.at)
			}
			for _, wk := range tc.wakes {
				check[wk.at] = wk
				times = append(times, wk.at-time.Second, wk.at)
			}
			for step := time.Duration(0); step <= DigestEvery+2*time.Minute; step += time.Second {
				for len(pending) > 0 && pending[0].at == step {
					w.clock = start.Add(step)
					if _, _, err := w.core.RecordGoalEvent(g.ID, pending[0].kind, pending[0].about, ""); err != nil {
						t.Fatal(err)
					}
					pending = pending[1:]
				}
				if !containsDuration(times, step) {
					continue
				}
				w.clock = start.Add(step)
				before := len(wakes(w.handedTo(m.Session)))
				w.loop(t, 1)
				got := wakes(w.handedTo(m.Session))
				wk, want := check[step]
				if !want {
					if len(got) != before {
						t.Fatalf("woken at +%s with %q, want no wake then", step, got[before:])
					}
					continue
				}
				if len(got) != before+1 {
					t.Fatalf("at +%s the manager has %d wakes, want one more", step, len(got))
				}
				for _, s := range wk.has {
					if !strings.Contains(got[before], s) {
						t.Fatalf("wake at +%s lacks %q:\n%s", step, s, got[before])
					}
				}
				for _, s := range wk.lack {
					if strings.Contains(got[before], s) {
						t.Fatalf("wake at +%s carries %q:\n%s", step, s, got[before])
					}
				}
			}
		})
	}
}

func containsDuration(ds []time.Duration, d time.Duration) bool {
	for _, x := range ds {
		if x == d {
			return true
		}
	}
	return false
}

func TestAWakeCarriesEachEventInOneShortLineAndYmGoalEventPrintsItWhole(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	long := "agent a1 (work a1) is idle. Its last message, verbatim: " + strings.Repeat("every detail of the parser, ", 200) + "THE END"
	e, _, err := w.core.RecordGoalEvent(g.ID, EventAgentHung, long, "")
	if err != nil {
		t.Fatal(err)
	}
	w.loop(t, 1)
	got := wakes(w.handedTo(m.Session))
	if len(got) != 1 {
		t.Fatalf("manager woke %d times, want once", len(got))
	}
	for _, line := range strings.Split(got[0], "\n") {
		if n := len([]rune(line)); n > lineMax {
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
			if tc.between != nil {
				tc.between(t, w)
			}
			before := len(w.handedTo(m.Session))
			reply(tc.second)
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
	got := wakes(w.handedTo(chief.Session))
	if len(got) != 1 || !strings.Contains(got[0], "your agent #") || !strings.Contains(got[0], "agent-finished: agent helper") || strings.Contains(got[0], long) {
		t.Fatalf("chief heard %q, want one wake with a line for its agent's reply, not the reply whole", got)
	}
}
