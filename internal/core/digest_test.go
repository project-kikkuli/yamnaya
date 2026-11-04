package core

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func digests(texts []string) []string {
	var out []string
	for _, t := range texts {
		if strings.Contains(t, "changes under you") {
			out = append(out, t)
		}
	}
	return out
}

func TestOnlyWhatNeedsAChiefWakesIt(t *testing.T) {
	url := "https://github.com/acme/august/pull/19637"
	for _, tc := range []struct {
		kind, payload string
		wakes         bool
	}{
		{EventFailedVerify, "attempt 1 failed: ci red", true},
		{EventFailedVerify, "attempt 1 withdrawn: the plan changed", false},
		{EventStuck, "quiet: nothing moved", true},
		{EventDone, "shipped", true},
		{EventVerified, "attempt 1 passed", true},
		{EventPRReady, `"Importer" ` + url + " is green and ready to merge", true},
		{EventResumeFailed, "agent a1's pane went and ym could not resume it", true},
		{EventRelayFailed, "ym goal send refused", true},
		{EventAgentStopped, "agent a1 (work) stopped: its process exited with status 143.", true},
		{EventAgentStopped, "agent a1 (work) stopped: its process exited with status 0.", false},
		{EventWorkResumed, "resumed", false},
		{EventSteered, "new plan", false},
		{EventAdopted, "adopted revision 2", false},
		{EventUpdate, "half done", false},
		{EventVerifying, "attempt 1", false},
		{EventReview, "2 threads", false},
		{EventCIGreen, "CI passed", false},
		{EventCIRed, "CI failed", false},
		{EventConflict, "conflicts with its base", false},
		{EventResumed, "agent a1's pane went and ym resumed it", false},
		{EventPROpened, "opened #7", false},
		{EventMerged, "merged #7", false},
		{EventAgentHung, "agent a1 looks hung", false},
		{EventAgentAdrift, "agent a1 is looping", false},
		{EventAgentAdrift, "manager mgr1 is looping", true},
		{EventRelayed, "ym goal relaunch mgr1 ran", true},
	} {
		t.Run(tc.kind+" "+tc.payload, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.clock = w.clock.Add(time.Minute)
			if _, _, err := w.core.RecordGoalEvent(g.ID, tc.kind, tc.payload, ""); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(2 * deathGrace)
			w.loop(t, 3)
			w.clock = w.clock.Add(24 * time.Hour)
			w.loop(t, 3)
			got := digests(w.handedTo(chief.Session))
			if tc.wakes != (len(got) == 1) || len(got) > 1 {
				t.Fatalf("chief got %q, wakes=%v", got, tc.wakes)
			}
		})
	}
}

func TestAStoppedAgentThatCameBackNeverReachesItsChief(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	w.clock = w.clock.Add(time.Minute)
	add := func(kind, text string) {
		if _, _, err := w.core.RecordGoalEvent(g.ID, kind, text, ""); err != nil {
			t.Fatal(err)
		}
	}
	add(EventAgentStopped, "agent a1 (work) stopped: its process exited with status 143.")
	w.loop(t, 2)
	if got := digests(w.handedTo(chief.Session)); len(got) != 0 {
		t.Fatalf("chief heard of a stop before ym had the chance to recover it: %q", got)
	}
	w.clock = w.clock.Add(deathGrace / 2)
	add(EventFinished, "agent a1 (work) is idle. Its last message, verbatim: back")
	w.clock = w.clock.Add(deathGrace)
	w.loop(t, 2)
	if got := digests(w.handedTo(chief.Session)); len(got) != 0 {
		t.Fatalf("chief was woken for an agent that recovered: %q", got)
	}
}

func TestWhatNeedsNoActionNeverStartsATurnAndIsReadOnDemand(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	w.clock = w.clock.Add(time.Minute)
	for _, kind := range []string{EventUpdate, EventUpdate, EventCIGreen} {
		if _, _, err := w.core.RecordGoalEvent(g.ID, kind, "x", ""); err != nil {
			t.Fatal(err)
		}
	}
	w.loop(t, 3)
	if got := digests(w.handedTo(chief.Session)); len(got) != 0 {
		t.Fatalf("quiet events started a message: %q", got)
	}
	lines, err := w.core.Digest(chief.Session)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{g.Name + ": 1 green CI run, 2 updates"}
	if !slices.Equal(lines, want) {
		t.Fatalf("ym digest printed %q, want %q", lines, want)
	}
	if lines, _ := w.core.Digest(chief.Session); len(lines) != 0 {
		t.Fatalf("a second ym digest printed %q again", lines)
	}
}

func TestQuietNewsRidesAlongAtMostEveryDigestPeriod(t *testing.T) {
	w := newOrgWorld(t)
	w.core.Pace = &WakePace{Digest: DigestEvery}
	g, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	event := func(kind string) {
		t.Helper()
		if _, _, err := w.core.RecordGoalEvent(g.ID, kind, "x", ""); err != nil {
			t.Fatal(err)
		}
	}
	w.clock = w.clock.Add(time.Minute)
	event(EventUpdate)
	w.clock = w.clock.Add(DigestEvery)
	event(EventStuck)
	w.loop(t, 1)
	first := digests(w.handedTo(chief.Session))
	if len(first) != 1 || !strings.Contains(first[0], "1 update") || !strings.Contains(first[0], "stuck #") {
		t.Fatalf("the wake carried %q, want the stuck and the update riding along", first)
	}
	w.clock = w.clock.Add(time.Minute)
	event(EventUpdate)
	w.clock = w.clock.Add(DigestEvery - 2*time.Minute)
	if lines, err := w.core.ChangesDue(chief.Session); err != nil || len(lines) != 0 {
		t.Fatalf("ym item inbox handed %q within a digest period of the last, err %v", lines, err)
	}
	w.clock = w.clock.Add(2 * time.Minute)
	if lines, err := w.core.ChangesDue(chief.Session); err != nil || !slices.Equal(lines, []string{g.Name + ": 1 update"}) {
		t.Fatalf("ym item inbox handed %q after a digest period, err %v", lines, err)
	}
}

func TestStuckAndFailedVerifyWakeOnce(t *testing.T) {
	findings := func(n int, what string) string {
		return fmt.Sprintf("attempt %d failed, so the goal is active again:\n  - %s", n, what)
	}
	for _, tc := range []struct {
		name   string
		events [][2]string
		want   []string
	}{
		{"stuck again without moving", [][2]string{{EventStuck, "a"}, {EventNudge, "n"}, {EventResumed, "r"}, {EventStuck, "b"}}, []string{"stuck"}},
		{"stuck again after moving", [][2]string{{EventStuck, "a"}, {EventUpdate, "moved"}, {EventStuck, "b"}}, []string{"stuck", "stuck"}},
		{"verification retried on the same cause", [][2]string{{EventFailedVerify, findings(1, "ci red")}, {EventFailedVerify, findings(2, "ci red")}, {EventFailedVerify, findings(3, "ci red")}}, []string{"failed verify"}},
		{"the same cause across the old and new wording", [][2]string{{EventFailedVerify, findings(1, "ci red")}, {EventFailedVerify, "attempt 2 failed:\n  - ci red" + failedVerifyEnd}}, []string{"failed verify"}},
		{"verification failed on a new cause", [][2]string{{EventFailedVerify, findings(1, "ci red")}, {EventFailedVerify, findings(2, "thread open")}}, []string{"failed verify", "failed verify"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			for _, e := range tc.events {
				w.clock = w.clock.Add(time.Minute)
				if _, _, err := w.core.RecordGoalEvent(g.ID, e[0], e[1], ""); err != nil {
					t.Fatal(err)
				}
				w.loop(t, 1)
			}
			var got []string
			for _, text := range digests(w.handedTo(chief.Session)) {
				for _, want := range []string{"stuck #", "failed verify #"} {
					got = append(got, slices.Repeat([]string{strings.TrimSuffix(want, " #")}, strings.Count(text, want))...)
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("chief was woken for %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAChiefMidTurnGetsOneDigestAndTheRestMergesIntoTheNext(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	w.set(chief.Session, func(s *Session) { s.Status, s.Since = Busy, w.clock })
	done := func(n int) {
		t.Helper()
		w.clock = w.clock.Add(time.Minute)
		if _, _, err := w.core.RecordGoalEvent(g.ID, EventDone, fmt.Sprintf("round %d", n), ""); err != nil {
			t.Fatal(err)
		}
		w.loop(t, 1)
	}
	done(1)
	done(2)
	done(3)
	got := digests(w.handedTo(chief.Session))
	if len(got) != 1 {
		t.Fatalf("a busy chief got %d digests, want 1: %q", len(got), got)
	}
	w.clock = w.clock.Add(time.Minute)
	w.set(chief.Session, func(s *Session) { s.Status, s.Since = Idle, w.clock })
	w.loop(t, 2)
	got = digests(w.handedTo(chief.Session))
	if len(got) != 2 || strings.Count(got[1], "\n"+g.Name+":") != 1 || strings.Count(got[1], "done #") != 2 {
		t.Fatalf("once idle the chief got %q, want one more message with both events on one line", got)
	}
}

func TestANewDigestMergesIntoOneStillPending(t *testing.T) {
	w := newOrgWorld(t)
	g1, m1 := w.goal(t, "ship the importer", "/repo/august")
	g2, m2 := w.goal(t, "rotate the keys", "/repo/auth")
	chief := w.chief(t, "platform", m1.ID, m2.ID)
	w.core.BackgroundSend = func(Live, string) error { return fmt.Errorf("pane busy") }
	pending := func() []Message {
		t.Helper()
		all, err := w.core.Store.Messages(chief.Session)
		if err != nil {
			t.Fatal(err)
		}
		return slices.DeleteFunc(all, func(m Message) bool { return m.State != MsgPending || !strings.HasPrefix(m.Text, digestHead) })
	}
	for _, step := range []struct {
		goal string
		kind string
	}{{g1.ID, EventStuck}, {g2.ID, EventDone}, {g1.ID, EventDone}} {
		w.clock = w.clock.Add(time.Minute)
		if _, _, err := w.core.RecordGoalEvent(step.goal, step.kind, "x", ""); err != nil {
			t.Fatal(err)
		}
		w.loop(t, 2)
	}
	got := pending()
	if len(got) != 1 {
		t.Fatalf("%d digests pending, want one holding everything: %+v", len(got), got)
	}
	for _, want := range []string{g1.Name + ": ", g2.Name + ": ", "stuck #", "done #"} {
		if !strings.Contains(got[0].Text, want) {
			t.Fatalf("pending digest lacks %q:\n%s", want, got[0].Text)
		}
	}
	if n := strings.Count(got[0].Text, g1.Name+":"); n != 1 {
		t.Fatalf("goal %s has %d lines, want 1:\n%s", g1.ID, n, got[0].Text)
	}
}

func TestADigestIsOneCompactLinePerGoal(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action []GoalEvent
		quiet  []GoalEvent
		want   []string
	}{
		{"counts collapse", nil, []GoalEvent{{Goal: "goal15", Kind: EventUpdate}, {Goal: "goal15", Kind: EventUpdate}, {Goal: "goal15", Kind: EventUpdate}}, []string{"goal15: 3 updates"}},
		{"a ready pull request by its number", []GoalEvent{{Goal: "goal15", Kind: EventPRReady, Seq: 9, Payload: `"T" https://github.com/acme/august/pull/19637 is green and ready to merge`}},
			[]GoalEvent{{Goal: "goal15", Kind: EventUpdate}, {Goal: "goal15", Kind: EventUpdate}, {Goal: "goal15", Kind: EventUpdate}}, []string{"goal15: acme/august PR 19637 ready, 3 updates"}},
		{"two repositories' pull request 19 stay apart", []GoalEvent{
			{Goal: "goal15", Kind: EventPRReady, Seq: 9, Payload: `"T" https://github.com/acme/august/pull/19 is green and ready to merge`},
			{Goal: "goal15", Kind: EventPRReady, Seq: 10, Payload: `"U" https://github.com/acme/yamnaya/pull/19 is green and ready to merge`}},
			nil, []string{"goal15: acme/august PR 19 ready, acme/yamnaya PR 19 ready"}},
		{"a title quoting another pull request", []GoalEvent{{Goal: "goal15", Kind: EventPRReady, Seq: 9, Payload: `"Backport of https://github.com/acme/a/pull/5" https://github.com/acme/b/pull/19 is green and ready to merge`}},
			nil, []string{"goal15: acme/b PR 19 ready"}},
		{"a new head of one pull request stays one action", []GoalEvent{
			{Goal: "goal15", Kind: EventPRReady, Seq: 9, Payload: `"T" https://github.com/acme/august/pull/19 is green and ready to merge`},
			{Goal: "goal15", Kind: EventPRReady, Seq: 12, Payload: `"T" https://github.com/acme/august/pull/19 is green and ready to merge`}},
			nil, []string{"goal15: acme/august PR 19 ready"}},
		{"what needs the chief sorts first", []GoalEvent{{Goal: "goal9", Kind: EventStuck, Seq: 4}},
			[]GoalEvent{{Goal: "goal1", Kind: EventCIGreen}, {Goal: "goal1", Kind: EventPROpened}}, []string{"goal9: stuck #4", "goal1: 1 green CI run, 1 opened PR"}},
		{"a long payload stays out of the line", []GoalEvent{{Goal: "goal2", Kind: EventFailedVerify, Seq: 7, Payload: strings.Repeat("finding ", 500)}}, nil, []string{"goal2: failed verify #7"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			label := func(e GoalEvent) string { return e.Goal }
			got := actionRollup(tc.action, label, "").merged(quietRollup(tc.quiet, label)).lines()
			if !slices.Equal(got, tc.want) {
				t.Fatalf("lines %q, want %q", got, tc.want)
			}
			if strings.Contains(strings.Join(got, ""), "…") {
				t.Fatalf("a line was cut: %q", got)
			}
		})
	}
}

func TestADigestOffersYmGoalEventOnlyForEventNumbers(t *testing.T) {
	ready := GoalEvent{Goal: "goal15", Kind: EventPRReady, Seq: 9, Payload: `"T" https://github.com/acme/august/pull/19 is green and ready to merge`}
	failed := GoalEvent{Goal: "goal15", Kind: EventFailedVerify, Seq: 812}
	label := func(e GoalEvent) string { return e.Goal }
	for _, tc := range []struct {
		name   string
		events []GoalEvent
		footer bool
	}{
		{"a ready pull request alone", []GoalEvent{ready}, false},
		{"beside a failed verification", []GoalEvent{ready, failed}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := actionRollup(tc.events, label, "").text(); strings.Contains(got, "ym goal event") != tc.footer || strings.Contains(got, "#19") {
				t.Fatalf("digest %q, want footer=%v and no #19", got, tc.footer)
			}
		})
	}
}

func TestReadyPullRequestsBecomeAnEventOncePerHead(t *testing.T) {
	url := "https://github.com/acme/august/pull/9"
	ready := PRStatus{State: "OPEN", Passed: 4, Merge: PRMergeFacts{OID: "h1", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	second := ready
	second.Merge.OID = "h2"
	red := ready
	red.Failed, red.Merge.MergeState = 1, "UNSTABLE"
	threads := ready
	threads.Threads, threads.Unresolved = 1, 1
	for _, tc := range []struct {
		name  string
		steps []PRStatus
		want  int
	}{
		{"ready stays one event", []PRStatus{ready, ready}, 1},
		{"a new head is ready anew", []PRStatus{ready, second}, 2},
		{"red is not ready", []PRStatus{red}, 0},
		{"an open thread is not ready", []PRStatus{threads}, 0},
		{"a resolved thread makes it ready", []PRStatus{threads, ready}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			for _, st := range tc.steps {
				w.pr(t, "agent", url, st)
				w.loop(t, 1)
			}
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(strings.Join(kinds(events), " "), EventPRReady); got != tc.want {
				t.Fatalf("%d ready events in %v, want %d", got, kinds(events), tc.want)
			}
		})
	}
}

func TestWhatAChiefsOwnAgentsDoWakesItAsItWouldAManager(t *testing.T) {
	for _, tc := range []struct {
		kind  string
		wakes bool
	}{
		{EventAsked, true},
		{EventFinished, true},
		{EventCIRed, true},
		{EventConflict, true},
		{EventReview, true},
		{EventAgentHung, true},
		{EventAgentAdrift, true},
		{EventCIGreen, false},
		{EventPROpened, false},
		{EventMerged, false},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.clock = w.clock.Add(time.Minute)
			if _, _, err := w.core.RecordGoalEvent(chief.ID, tc.kind, "agent a1 did it", ""); err != nil {
				t.Fatal(err)
			}
			w.loop(t, 2)
			got := digests(w.handedTo(chief.Session))
			if tc.wakes != (len(got) == 1) || len(got) > 1 {
				t.Fatalf("chief got %q, wakes=%v", got, tc.wakes)
			}
		})
	}
}

func TestAManagerMidTurnGetsOneWakeAndTheRestMergesIntoTheNext(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.set(m.Session, func(s *Session) { s.Status, s.Since = Busy, w.clock })
	red := func(what string) {
		t.Helper()
		w.clock = w.clock.Add(time.Minute)
		if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, what, ""); err != nil {
			t.Fatal(err)
		}
		w.loop(t, 1)
	}
	for _, what := range []string{"first", "second", "third"} {
		red(what)
	}
	if got := wakes(w.handedTo(m.Session)); len(got) != 1 || !strings.Contains(got[0], "first") {
		t.Fatalf("a busy manager got %q, want the first wake alone", got)
	}
	w.clock = w.clock.Add(time.Minute)
	w.set(m.Session, func(s *Session) { s.Status, s.Since = Idle, w.clock })
	w.loop(t, 1)
	got := wakes(w.handedTo(m.Session))
	if len(got) != 2 || !strings.Contains(got[1], "second") || !strings.Contains(got[1], "third") {
		t.Fatalf("once idle the manager got %q, want one more wake with both held events", got)
	}
}

func TestYmDigestLeavesAManagersEventsForItsWake(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.set(m.Session, func(s *Session) { s.Prompt = &Prompt{} })
	if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, "red", ""); err != nil {
		t.Fatal(err)
	}
	lines, err := w.core.Digest(m.Session)
	if err != nil || !slices.Equal(lines, []string{g.Name + ": 1 red CI run"}) {
		t.Fatalf("ym digest printed %q, err %v", lines, err)
	}
	w.set(m.Session, func(s *Session) { s.Prompt = nil })
	w.clock = w.clock.Add(time.Minute)
	w.loop(t, 1)
	if got := wakes(w.handedTo(m.Session)); len(got) != 1 {
		t.Fatalf("after ym digest the manager was woken with %q, want the red CI", got)
	}
}
