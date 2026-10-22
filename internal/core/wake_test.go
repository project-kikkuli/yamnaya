package core

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func (w *orgWorld) loop(t *testing.T, n int) {
	t.Helper()
	for range n {
		if err := w.core.Loop(w.board(t)); err != nil {
			t.Fatal(err)
		}
	}
}

func (w *orgWorld) pr(t *testing.T, session, url string, st PRStatus) {
	t.Helper()
	w.set(session, func(s *Session) {
		if !slices.ContainsFunc(s.PRs, func(p PR) bool { return p.URL == url }) {
			s.PRs = append(s.PRs, PR{URL: url, Repo: "acme/august"})
		}
	})
	st.Fetched = w.clock
	if err := w.core.Store.SavePRStatus(url, st); err != nil {
		t.Fatal(err)
	}
}

func kinds(events []GoalEvent) []string {
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func wakes(texts []string) []string {
	var out []string
	for _, t := range texts {
		if strings.Contains(t, "new event") || strings.Contains(t, "under you") {
			out = append(out, t)
		}
	}
	return out
}

func TestAGoalsEventsWakeItsManagerOnceInOneMessageAcrossRestartsAndRelaunches(t *testing.T) {
	w := newOrgWorld(t)
	path := filepath.Join(t.TempDir(), "ym.db")
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	w.core.Store = st
	t.Cleanup(func() { w.core.Store.Close() })
	g, m := w.goal(t, "make the importer handle CSV", "/repo/august")
	w.spawn("agent", m.Session)
	w.set("agent", func(s *Session) { s.Result, s.ResultAt = "Parser done, PR is up.", w.clock })
	url := "https://github.com/acme/august/pull/7"
	w.pr(t, "agent", url, PRStatus{State: "OPEN", Title: "CSV importer", Passed: 4, Merge: PRMergeFacts{OID: "abc"}})
	w.loop(t, 3)
	got := wakes(w.handedTo(m.Session))
	if len(got) != 1 {
		t.Fatalf("manager woke %d times, want once with every event: %q", len(got), got)
	}
	for _, want := range []string{"agent-finished: agent agent", "Parser done, PR is up.", "pr-opened:", "ci-green: CI passed on \"CSV importer\""} {
		if !strings.Contains(got[0], want) {
			t.Fatalf("wake lacks %q:\n%s", want, got[0])
		}
	}

	st.Close()
	if w.core.Store, err = OpenStore(path); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 2)
	if got := wakes(w.handedTo(m.Session)); len(got) != 1 {
		t.Fatalf("a restart woke the manager again: %q", got[1:])
	}

	w.stop(m.Session)
	w.clock = w.clock.Add(StartGrace + time.Minute)
	w.pr(t, "agent", url, PRStatus{State: "MERGED", Title: "CSV importer", Passed: 4, Merge: PRMergeFacts{OID: "abc"}})
	w.loop(t, 1)
	relaunched, err := w.core.Store.Manager(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if relaunched.Session == m.Session {
		t.Fatal("the dead manager was not relaunched")
	}
	prompt := w.launches.starts[len(w.launches.starts)-1].Prompt
	if !strings.Contains(prompt, "merged: merged \"CSV importer\"") {
		t.Fatalf("the relaunch prompt does not carry the merge that happened while the manager was down:\n%s", prompt)
	}
	w.loop(t, 3)
	if got := wakes(w.handedTo(relaunched.Session)); len(got) != 0 {
		t.Fatalf("the relaunched manager was woken again with what its prompt carried: %q", got)
	}
	events, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		if e.Delivered.IsZero() {
			t.Fatalf("event %s %q was never handed to the manager", e.Kind, e.Payload)
		}
	}
}

func TestPullRequestStatesBecomeOneEventEach(t *testing.T) {
	url := "https://github.com/acme/august/pull/9"
	for _, tc := range []struct {
		name  string
		steps []PRStatus
		want  []string
	}{
		{"opened, CI still running", []PRStatus{{State: "OPEN", Pending: 2}}, []string{EventPROpened}},
		{"red then green on a new head", []PRStatus{
			{State: "OPEN", Failed: 1, Passed: 3, Merge: PRMergeFacts{OID: "h1"}},
			{State: "OPEN", Pending: 1, Merge: PRMergeFacts{OID: "h2"}},
			{State: "OPEN", Passed: 4, Merge: PRMergeFacts{OID: "h2"}},
		}, []string{EventPROpened, EventCIRed, EventCIGreen}},
		{"a red head stays one event", []PRStatus{
			{State: "OPEN", Failed: 1, Merge: PRMergeFacts{OID: "h1"}},
			{State: "OPEN", Failed: 1, Merge: PRMergeFacts{OID: "h1"}},
		}, []string{EventPROpened, EventCIRed}},
		{"review threads grow, then approval", []PRStatus{
			{State: "OPEN", Threads: 1, Unresolved: 1, Merge: PRMergeFacts{OID: "h1"}},
			{State: "OPEN", Threads: 1, Unresolved: 0, Merge: PRMergeFacts{OID: "h1"}},
			{State: "OPEN", Threads: 3, Unresolved: 2, Merge: PRMergeFacts{OID: "h1"}},
			{State: "OPEN", Threads: 3, Review: "APPROVED", Merge: PRMergeFacts{OID: "h1"}},
		}, []string{EventPROpened, EventReview, EventReview, EventReview}},
		{"merged", []PRStatus{{State: "OPEN", Pending: 1}, {State: "MERGED", Passed: 2}}, []string{EventPROpened, EventMerged}},
		{"closed", []PRStatus{{State: "CLOSED"}}, []string{EventPROpened, EventClosed}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			w.set("agent", func(s *Session) { s.Status = Busy })
			for _, st := range tc.steps {
				w.pr(t, "agent", url, st)
				w.loop(t, 2)
			}
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := kinds(events); !slices.Equal(got, tc.want) {
				t.Fatalf("events %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAManagerThatEndsItsTurnWithNothingInFlightIsNudgedOnce(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(t *testing.T, w *orgWorld, m Manager)
		nudged bool
	}{
		{"nothing in flight", func(*testing.T, *orgWorld, Manager) {}, true},
		{"an agent works", func(t *testing.T, w *orgWorld, m Manager) {
			w.spawn("agent", m.Session)
			w.set("agent", func(s *Session) { s.Status = Busy })
		}, false},
		{"a pull request awaits CI", func(t *testing.T, w *orgWorld, m Manager) {
			w.spawn("agent", m.Session)
			w.pr(t, "agent", "https://github.com/acme/august/pull/3", PRStatus{State: "OPEN", Pending: 3})
		}, false},
		{"a wake is set", func(t *testing.T, w *orgWorld, m Manager) {
			if _, err := w.core.SetWake(m.Session, w.clock.Add(time.Hour), "check the deploy"); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"it waits on the owner", func(t *testing.T, w *orgWorld, m Manager) { w.ask(t, m.Session, "may I drop the old API?") }, false},
		{"an agent's ask waits on it", func(t *testing.T, w *orgWorld, m Manager) {
			w.spawn("agent", m.Session)
			w.ask(t, "agent", "CSV or TSV?")
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			tc.setup(t, w, m)
			w.loop(t, 1)
			w.clock = w.clock.Add(time.Minute)
			w.set(m.Session, func(s *Session) { s.Result, s.ResultAt = "Started the work.", w.clock })
			w.loop(t, 3)
			var nudges []string
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, "nothing in flight") {
					nudges = append(nudges, text)
				}
			}
			if want := map[bool]int{true: 1, false: 0}[tc.nudged]; len(nudges) != want {
				t.Fatalf("manager got %d nudges, want %d: %q", len(nudges), want, nudges)
			}
			w.clock = w.clock.Add(time.Minute)
			w.set(m.Session, func(s *Session) { s.ResultAt = w.clock })
			w.loop(t, 1)
			events, _ := w.core.Store.GoalEvents(g.ID)
			if n := strings.Count(strings.Join(kinds(events), " "), EventNudge); tc.nudged && n != 2 {
				t.Fatalf("a second idle turn with nothing in flight got %d nudges in all, want 2", n)
			}
		})
	}
}

func TestAWakeAManagerSetFiresOnceWhenDue(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "watch the rollout", "/repo/august")
	due, err := ParseWhen("30m", w.clock)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.SetWake(m.Session, due, "check the canary error rate"); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 2)
	if got := wakes(w.handedTo(m.Session)); len(got) != 0 {
		t.Fatalf("woken before the wake was due: %q", got)
	}
	w.clock = w.clock.Add(31 * time.Minute)
	w.loop(t, 3)
	got := wakes(w.handedTo(m.Session))
	if len(got) != 1 || !strings.Contains(got[0], "timer:") || !strings.Contains(got[0], "check the canary error rate") {
		t.Fatalf("woken %q, want once with the reason", got)
	}
	if _, err := w.core.SetWake("agent-x", due, "x"); err == nil {
		t.Fatal("a session that is no manager set a wake")
	}
}

func TestParseWhenReadsDurationsClockTimesAndRFC3339(t *testing.T) {
	now := time.Date(2026, 10, 22, 14, 0, 0, 0, time.Local)
	for _, tc := range []struct {
		in   string
		want time.Time
	}{
		{"45m", now.Add(45 * time.Minute)},
		{"2h", now.Add(2 * time.Hour)},
		{"15:30", time.Date(2026, 10, 22, 15, 30, 0, 0, time.Local)},
		{"9:00", time.Date(2026, 10, 23, 9, 0, 0, 0, time.Local)},
		{"3pm", time.Date(2026, 10, 22, 15, 0, 0, 0, time.Local)},
		{"2026-10-24T08:00:00Z", time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC)},
	} {
		got, err := ParseWhen(tc.in, now)
		if err != nil || !got.Equal(tc.want) {
			t.Errorf("ParseWhen(%q) = %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{"", "-5m", "soon"} {
		if _, err := ParseWhen(bad, now); err == nil {
			t.Errorf("ParseWhen(%q) took it", bad)
		}
	}
}

func TestNestedChiefsHearEveryStateChangeBelowThemBatchedAndNeverGateWork(t *testing.T) {
	w := newOrgWorld(t)
	g1, m1 := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
	g2, m2 := w.goal(t, "make the importer handle CSV", "/repo/august")
	lower := w.chief(t, "auth", m1.ID)
	upper := w.chief(t, "platform", lower.ID, m2.ID)
	w.spawn("a1", m1.Session)
	w.spawn("a2", m2.Session)
	w.set("a1", func(s *Session) { s.Result, s.ResultAt = "keys rotated", w.clock })
	w.pr(t, "a1", "https://github.com/acme/auth-plane/pull/1", PRStatus{State: "OPEN", Passed: 2, Merge: PRMergeFacts{OID: "k1"}})
	w.pr(t, "a2", "https://github.com/acme/august/pull/2", PRStatus{State: "OPEN", Failed: 1, Merge: PRMergeFacts{OID: "c1"}})
	if _, err := w.core.UpdateGoal(m2.Session, "parser half done, CI red on a flaky test"); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 3)

	lowerGot := wakes(w.handedTo(lower.Session))
	if len(lowerGot) != 1 || !strings.Contains(lowerGot[0], g1.ID) || strings.Contains(lowerGot[0], g2.ID) {
		t.Fatalf("chief auth heard %q, want one batch about %s alone", lowerGot, g1.ID)
	}
	upperGot := wakes(w.handedTo(upper.Session))
	if len(upperGot) != 1 {
		t.Fatalf("chief platform heard %d batches, want one: %q", len(upperGot), upperGot)
	}
	for _, want := range []string{g1.ID + " (" + m1.ID, "ci-green", g2.ID + " (" + m2.ID, "ci-red", "update: parser half done"} {
		if !strings.Contains(upperGot[0], want) {
			t.Fatalf("chief platform's batch lacks %q:\n%s", want, upperGot[0])
		}
	}
	for _, chief := range []string{lowerGot[0], upperGot[0]} {
		if strings.Contains(chief, "keys rotated") {
			t.Fatalf("a chief heard an agent's reply, which only its manager needs:\n%s", chief)
		}
	}
	if got := wakes(w.handedTo(m2.Session)); len(got) != 1 || strings.Contains(got[0], "parser half done") {
		t.Fatalf("manager %s was woken with %q; its own update never wakes it", m2.ID, got)
	}
	it := w.ask(t, "a1", "keep the old key for a day?")
	if it.Recipient != m1.ID {
		t.Fatalf("an agent's ask went to %q with chiefs above, want its manager %s", it.Recipient, m1.ID)
	}
}

func TestTheOwnersFeedCountsUnreadNewsAndShowsTheNewestLine(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.spawn("agent", m.Session)
	w.set("agent", func(s *Session) { s.Result, s.ResultAt = "parser done", w.clock })
	w.pr(t, "agent", "https://github.com/acme/august/pull/4", PRStatus{State: "OPEN", Passed: 1, Merge: PRMergeFacts{OID: "x"}})
	w.loop(t, 1)
	w.clock = w.clock.Add(time.Minute)
	if _, err := w.core.UpdateGoal(m.Session, "importer reads CSV; TSV next"); err != nil {
		t.Fatal(err)
	}
	node := func() Node {
		n, ok := w.board(t).Node(m.ID)
		if !ok {
			t.Fatal("goal left the board")
		}
		return n
	}
	n := node()
	if n.Feed.Unread != 2 || n.Feed.Latest == nil || n.Feed.Latest.Payload != "importer reads CSV; TSV next" {
		t.Fatalf("feed unread %d latest %+v, want 2 (PR opened, update) and the update newest", n.Feed.Unread, n.Feed.Latest)
	}
	if len(n.Feed.Recent) != 4 {
		t.Fatalf("feed shows %v, want the agent reply and green CI too", kinds(n.Feed.Recent))
	}
	chief := w.chief(t, "platform", m.ID)
	if c, _ := w.board(t).Node(chief.ID); c.Unread() != 2 {
		t.Fatalf("chief row unread %d, want its goal's 2", c.Unread())
	}
	if err := w.core.Store.MarkSeen(g.ID, w.clock); err != nil {
		t.Fatal(err)
	}
	if n := node(); n.Feed.Unread != 0 || n.Feed.Latest == nil {
		t.Fatalf("after reading, unread %d latest %v; want 0 and the newest line kept", n.Feed.Unread, n.Feed.Latest)
	}
}

func TestOnlyDoneAndStuckRaiseADesktopNotificationEachOnce(t *testing.T) {
	w := newOrgWorld(t)
	var shown []string
	w.core.Notify = func(title, body string) error { shown = append(shown, title+" | "+body); return nil }
	gDone, mDone := w.goal(t, "ship the importer", "/repo/august")
	gStuck, _ := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
	w.spawn("agent", mDone.Session)
	w.pr(t, "agent", "https://github.com/acme/august/pull/5", PRStatus{State: "MERGED", Passed: 3})
	if _, err := w.core.UpdateGoal(mDone.Session, "merged; wrapping up"); err != nil {
		t.Fatal(err)
	}
	w.core.Landings = fakeLandings{"https://github.com/acme/august/pull/5": {State: "MERGED", MergeCommit: "abc123", Passed: 3}}
	if _, _, err := w.core.FinishGoal(mDone.Session, "importer ships CSV", nil); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 1)
	if len(shown) != 0 {
		t.Fatalf("notified %q while the goal was only being verified", shown)
	}
	var verifier string
	for _, s := range w.launches.starts {
		if strings.HasPrefix(s.Slug, "verify-") {
			verifier = s.Session
		}
	}
	if _, err := w.core.RecordVerdict(verifier, gDone.ID, VerdictPass, nil); err != nil {
		t.Fatal(err)
	}
	w.launches.fail = fmt.Errorf("tmux is gone")
	stuck, _ := w.core.Store.Goal(gStuck.ID)
	mStuck, _ := w.core.Store.Manager(stuck.ManagerID)
	w.stop(mStuck.Session)
	for range 4 {
		w.clock = w.clock.Add(3 * time.Hour)
		w.loop(t, 1)
	}
	want := []string{"ym: " + gDone.ID + " done | ship the importer: importer ships CSV", "ym: " + gStuck.ID + " stuck"}
	if len(shown) != 2 || shown[0] != want[0] || !strings.HasPrefix(shown[1], want[1]) {
		t.Fatalf("notifications %q, want exactly %q then one for %s stuck", shown, want[0], gStuck.ID)
	}
}

func TestAPullRequestStaysWatchedAfterTheAgentThatOpenedItEnds(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.spawn("agent", m.Session)
	url := "https://github.com/acme/august/pull/11"
	w.pr(t, "agent", url, PRStatus{State: "OPEN", Pending: 2, Merge: PRMergeFacts{OID: "h1"}})
	w.loop(t, 1)
	w.stop("agent")
	w.pr(t, m.Session, "https://github.com/acme/august/pull/12", PRStatus{State: "OPEN", Pending: 1})
	if err := w.core.Store.SavePRStatus(url, PRStatus{State: "MERGED", Passed: 3, Fetched: w.clock}); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 2)
	events, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(events); !slices.Contains(got, EventMerged) {
		t.Fatalf("events %v: the merge of a pull request whose agent ended went unnoticed", got)
	}
}
