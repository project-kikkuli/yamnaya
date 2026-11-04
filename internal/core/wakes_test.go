package core

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const briefLine = "Your brief is behind"

func countLines(texts []string, what string) int {
	n := 0
	for _, text := range texts {
		n += strings.Count(text, what)
	}
	return n
}

func TestABriefBehindIsOneLineOfTheNextWakeAndNeverAMessageOfItsOwn(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	if _, err := w.core.WriteBrief(m.Session, brief("open the PR")); err != nil {
		t.Fatal(err)
	}
	event := func(kind, what string) {
		t.Helper()
		w.clock = w.clock.Add(time.Minute)
		if _, _, err := w.core.RecordGoalEvent(g.ID, kind, what, ""); err != nil {
			t.Fatal(err)
		}
	}
	w.core.Pace = &WakePace{Debounce: time.Minute, Reply: 10 * time.Minute, Digest: DigestEvery}
	event(EventFinished, "agent a1 is idle. Its last message, verbatim: pushed")
	for range 3 {
		w.clock = w.clock.Add(2 * time.Minute)
		w.loop(t, 1)
	}
	if got := w.handedTo(m.Session); len(got) != 0 {
		t.Fatalf("a stale brief sent %q before the reply's wake was due", got)
	}
	w.clock = w.clock.Add(5 * time.Minute)
	w.loop(t, 2)
	got := w.handedTo(m.Session)
	if len(got) != 1 || countLines(got, briefLine) != 1 || !strings.Contains(got[0], "agent-finished") {
		t.Fatalf("handed %q, want one wake carrying the reply and the brief line", got)
	}
	for _, line := range strings.Split(got[0], "\n") {
		if len([]rune(line)) > lineMax {
			t.Fatalf("line over %d characters: %q", lineMax, line)
		}
	}
	event(EventTimer, "the wake you set")
	w.clock = w.clock.Add(2 * time.Minute)
	w.set(m.Session, func(s *Session) { s.Since = w.clock })
	w.loop(t, 1)
	got = w.handedTo(m.Session)
	if len(got) != 2 || countLines(got, briefLine) != 1 {
		t.Fatalf("handed %q, want the second wake to ask nothing again for the same event", got)
	}
	event(EventCIRed, "CI failed")
	w.clock = w.clock.Add(2 * time.Minute)
	w.loop(t, 1)
	got = w.handedTo(m.Session)
	if len(got) != 3 || countLines(got, briefLine) != 2 {
		t.Fatalf("handed %q, want a newer meaningful event to ask once more", got)
	}
	w.clock = w.clock.Add(time.Minute)
	if _, err := w.core.WriteBrief(m.Session, brief("fix CI")); err != nil {
		t.Fatal(err)
	}
	event(EventTimer, "another wake you set")
	w.clock = w.clock.Add(2 * time.Minute)
	w.loop(t, 1)
	if got = w.handedTo(m.Session); countLines(got, briefLine) != 2 {
		t.Fatalf("handed %q, a fresh brief still drew a request", got)
	}
}

func TestAManagerWithNoBriefHearsItOnlyInsideAWake(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.clock = w.clock.Add(time.Hour)
	w.supervise(t)
	if got := w.handedTo(m.Session); countLines(got, briefLine) != 0 {
		t.Fatalf("the watch sent %q by itself", got)
	}
	if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, "CI failed", ""); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 1)
	if got := wakes(w.handedTo(m.Session)); len(got) != 1 || !strings.Contains(got[0], "you have written none yet") {
		t.Fatalf("wakes %q, want the wake to ask for a first brief", got)
	}
}

func TestAChiefIsAskedForABriefOnlyForWhatItsOwnBriefMissed(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(t *testing.T, w *orgWorld, chief, m Manager)
		asked bool
	}{
		{"a report rewrote its brief", func(t *testing.T, w *orgWorld, chief, m Manager) {
			if _, err := w.core.WriteBrief(chief.Session, brief("watching")); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Minute)
			if _, err := w.core.WriteBrief(m.Session, brief("moved")); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"it never wrote one", func(t *testing.T, w *orgWorld, chief, m Manager) {}, true},
		{"it adopted a steer since", func(t *testing.T, w *orgWorld, chief, m Manager) {
			if _, err := w.core.WriteBrief(chief.Session, brief("watching")); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Minute)
			st := w.steer(t, chief.ID, "watch the importer only", Owner)
			w.clock = w.clock.Add(time.Minute)
			if _, err := w.core.Adopt(chief.Session, chief.ID, st.Revision); err != nil {
				t.Fatal(err)
			}
		}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			if _, err := w.core.WriteBrief(m.Session, brief("start")); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Minute)
			tc.setup(t, w, chief, m)
			w.clock = w.clock.Add(BriefGrace + time.Minute)
			w.supervise(t)
			if got := w.handedTo(chief.Session); countLines(got, briefLine) != 0 {
				t.Fatalf("the watch told the chief %q by itself", got)
			}
			if _, _, err := w.core.RecordGoalEvent(g.ID, EventVerified, "attempt 1 passed", ""); err != nil {
				t.Fatal(err)
			}
			w.loop(t, 2)
			got := digests(w.handedTo(chief.Session))
			if len(got) != 1 || (countLines(got, briefLine) == 1) != tc.asked {
				t.Fatalf("digests %q, want one, asking for a brief=%v", got, tc.asked)
			}
		})
	}
}

func TestAReportStillShowsAChiefsBriefStaleOnTheBoard(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	if _, err := w.core.WriteBrief(chief.Session, brief("watching")); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(time.Minute)
	if _, err := w.core.WriteBrief(m.Session, brief("moved")); err != nil {
		t.Fatal(err)
	}
	o, err := w.core.LoadOrg()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := w.core.Freshness(o)
	if err != nil {
		t.Fatal(err)
	}
	if !fresh[chief.ID].Stale() {
		t.Fatalf("freshness %+v, want the chief's brief stale on the board", fresh[chief.ID])
	}
}

func TestATerminalReportWakesItsManagerOnTheOneMinuteDebounce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		first func(t *testing.T, w *orgWorld, m Manager, id string, report int64)
		woken bool
	}{
		{"left alone", func(t *testing.T, w *orgWorld, m Manager, id string, report int64) {}, true},
		{"accepted before its wake", func(t *testing.T, w *orgWorld, m Manager, id string, report int64) {
			if err := w.core.AcceptAssignment(m.Session, id, report); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Pace = nil
			_, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			a, err := w.core.AssignWork(m.Session, "agent", "work-1", "the parser")
			if err != nil {
				t.Fatal(err)
			}
			report, err := w.core.ReportAssignment("agent", a.ID, "parser pushed")
			if err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(WakeDebounce - time.Second)
			w.loop(t, 1)
			if got := wakes(w.handedTo(m.Session)); len(got) != 0 {
				t.Fatalf("woken before the debounce: %q", got)
			}
			tc.first(t, w, m, a.ID, report)
			w.clock = w.clock.Add(time.Second)
			w.loop(t, 1)
			got := wakes(w.handedTo(m.Session))
			if tc.woken != (len(got) == 1) || tc.woken && !strings.Contains(got[0], "1 for you") {
				t.Fatalf("wakes %q, want woken=%v", got, tc.woken)
			}
		})
	}
}

func TestWhatAManagerOrChiefPulledIsNotHandedToItAgain(t *testing.T) {
	for _, tc := range []struct {
		name         string
		kind, says   string
		reader       func(m, chief Manager) string
		managerWoken bool
		chiefDigest  bool
	}{
		{"its manager read a reply", EventFinished, "agent a1 is idle. Its last message, verbatim: pushed", func(m, _ Manager) string { return m.Session }, false, false},
		{"its manager read a quiet event", EventCIGreen, "CI passed", func(m, _ Manager) string { return m.Session }, false, true},
		{"its manager read what wakes it", EventCIRed, "CI failed", func(m, _ Manager) string { return m.Session }, true, true},
		{"an agent read it", EventCIGreen, "CI passed", func(Manager, Manager) string { return "agent" }, true, true},
		{"its chief read a quiet event", EventCIGreen, "CI passed", func(_, chief Manager) string { return chief.Session }, true, false},
		{"its chief read what needs it", EventVerified, "attempt 1 passed", func(_, chief Manager) string { return chief.Session }, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Pace = nil
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.spawn("agent", m.Session)
			w.clock = w.clock.Add(time.Minute)
			e, _, err := w.core.RecordGoalEvent(g.ID, tc.kind, tc.says, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := w.core.NotePulled(tc.reader(m, chief), []GoalEvent{e}); err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(DigestEvery + time.Minute)
			w.loop(t, 2)
			undelivered, err := w.core.Store.undelivered(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if woken := strings.Contains(strings.Join(w.handedTo(m.Session), "\n"), tc.says); woken != tc.managerWoken {
				t.Fatalf("manager handed %q (undelivered %v), want the event handed=%v", w.handedTo(m.Session), undelivered, tc.managerWoken)
			}
			pending, err := w.core.chiefEvents(mustOrg(t, w), chief)
			if err != nil {
				t.Fatal(err)
			}
			row, err := w.core.Store.digest(chief.ID)
			if err != nil {
				t.Fatal(err)
			}
			heard := len(pending) > 0 || len(row.Quiet) > 0 || len(row.Action) > 0 || len(digests(w.handedTo(chief.Session))) > 0
			if heard != tc.chiefDigest {
				t.Fatalf("chief pending %v, row %+v, handed %q, want it to hear=%v", pending, row, w.handedTo(chief.Session), tc.chiefDigest)
			}
		})
	}
}

func mustOrg(t *testing.T, w *orgWorld) Org {
	t.Helper()
	o, err := w.core.LoadOrg()
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestTheReplyRightBehindATerminalReportIsCountedUnlessItRaisesSomethingNew(t *testing.T) {
	const summary = "Parser pushed as a1b2c3d, suite passed."
	for _, tc := range []struct {
		name    string
		after   time.Duration
		reply   string
		before  func(t *testing.T, w *orgWorld, commit func(at time.Time))
		between func(t *testing.T, w *orgWorld, commit func(at time.Time))
		news    bool
		summary string
	}{
		{name: "restating the report", after: 20 * time.Second, reply: "The parser is pushed and the report is filed, waiting for your accept.", news: false},
		{name: "with a commit made before the report", after: 20 * time.Second, reply: "The parser is pushed and the report is filed.", news: false,
			before: func(t *testing.T, w *orgWorld, commit func(time.Time)) { commit(w.clock.Add(-time.Minute)) }},
		{name: "telling the manager not to accept", after: 47 * time.Second, reply: "Don't accept that report as written: it holds placeholder text.", news: true},
		{name: "owning up to a mistake", after: 47 * time.Second, reply: "The report holds my mistake; the push went to another branch.", news: true},
		{name: "a failure the report does not mention", after: time.Minute, reply: "The report is filed, but the suite failed on main afterwards.", news: true},
		{name: "a question", after: time.Minute, reply: "The report is filed. Should I also delete the branch?", news: true},
		{name: "with a commit made after the report", after: time.Minute, reply: "The report is filed and the parser is pushed.", news: true,
			between: func(t *testing.T, w *orgWorld, commit func(time.Time)) { commit(w.clock.Add(30 * time.Second)) }},
		{name: "with an ask filed after the report", after: time.Minute, reply: "The report is filed and the parser is pushed.", news: true,
			between: func(t *testing.T, w *orgWorld, _ func(time.Time)) {
				w.clock = w.clock.Add(time.Second)
				w.ask(t, "agent", "may I delete the branch?")
			}},
		{name: "restating a failure the report names", after: 20 * time.Second, reply: "Parser pushed, suite passed. Report filed.", news: false},
		{name: "a new failure when the report names another", after: time.Minute, reply: "The report is filed, but the push to main failed.", news: true, summary: "Fixed the failing importer test."},
		{name: "repeating a failure the report names", after: time.Minute, reply: "The importer test is failing on main.", news: false, summary: "I stopped. The importer test is failing on main. Nothing else moved."},
		{name: "much longer than the report", after: time.Minute, reply: strings.Repeat("Separately the exporter drops quoted rows when columns repeat, and the fixtures disagree about dialects today. ", 4), news: true},
		{name: "too long after the report", after: reportEcho + time.Minute, reply: "Separately, the CSV dialect question is settled.", news: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			repo := t.TempDir()
			git := func(env []string, args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
				cmd.Env = append(os.Environ(), env...)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, out)
				}
			}
			git(nil, "init", "-q")
			n := 0
			commit := func(at time.Time) {
				t.Helper()
				n++
				name := "f" + strings.Repeat("x", n)
				if err := os.WriteFile(filepath.Join(repo, name), []byte(name), 0o644); err != nil {
					t.Fatal(err)
				}
				git(nil, "add", name)
				date := at.UTC().Format(time.RFC3339)
				git([]string{"GIT_COMMITTER_DATE=" + date, "GIT_AUTHOR_DATE=" + date}, "commit", "-q", "-m", name)
			}
			commit(w.clock.Add(-time.Hour))
			w.set("agent", func(s *Session) { s.Worktree, s.Result, s.ResultAt = repo, "Starting the parser.", w.clock })
			w.loop(t, 1)
			a, err := w.core.AssignWork(m.Session, "agent", "work-1", "the parser")
			if err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(time.Hour)
			if tc.before != nil {
				tc.before(t, w, commit)
			}
			said := summary
			if tc.summary != "" {
				said = tc.summary
			}
			if _, err := w.core.ReportAssignment("agent", a.ID, said); err != nil {
				t.Fatal(err)
			}
			if tc.between != nil {
				tc.between(t, w, commit)
			}
			at := w.clock.Add(tc.after)
			w.set("agent", func(s *Session) { s.Result, s.ResultAt = tc.reply, at })
			w.clock = at
			w.loop(t, 1)
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			var last GoalEvent
			for _, e := range events {
				if e.Kind == EventIdleAgain || e.Kind == EventFinished {
					last = e
				}
			}
			if !strings.Contains(last.Payload, tc.reply) {
				t.Fatalf("the reply was not recorded: %+v", events)
			}
			if got := last.Kind == EventFinished; got != tc.news {
				t.Fatalf("recorded as %s, want news=%v", last.Kind, tc.news)
			}
			if tc.news {
				return
			}
			w.clock = w.clock.Add(time.Minute)
			w.set("agent", func(s *Session) { s.Result, s.ResultAt = "Picked up a new unit: the TSV exporter is next.", w.clock })
			w.loop(t, 1)
			events, err = w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range events {
				if strings.Contains(e.Payload, "TSV") && e.Kind != EventFinished {
					t.Fatalf("the reply after the echo was recorded as %s, want it judged as usual", e.Kind)
				}
			}
		})
	}
}

func TestOnlyABareAcknowledgementIsCounted(t *testing.T) {
	for _, tc := range []struct {
		reply string
		bare  bool
	}{
		{"Understood.", true},
		{"Got it", true},
		{"Claude: understood.", true},
		{"Understood. I'll push as soon as the hand-run checks finish.", true},
		{"Noted, standing by.", true},
		{"Understood. This unit is closed, and I have no jobs running or queued.", false},
		{"Understood. The migration corrupted the staging table.", false},
		{"OK, the deploy crashed on main.", false},
		{"Got it, the tests are broken on main.", false},
		{"Understood. I am abandoning this approach; it won't work.", false},
		{"Noted. I merged PR 412 to main.", false},
		{"Understood, you must choose between A and B.", false},
		{"Understood. I won't push.", false},
		{"Noted. A and B are fixed on 4de9969, and I had B only half right.", false},
		{"Understood. The poll unit is closed, and I'm starting nothing further. It stays local.", false},
		{"Understood. The task is closed: review is filed as R4.", false},
		{"Understood, but the suite failed on main.", false},
		{"Noted. The DB test failed on main's code.", false},
		{"Understood. Should I rebase first?", false},
		{"Your call: register a new assignment id.", false},
		{"Understood. " + strings.Repeat("Nothing else moves here ", 8), false},
		{"The parser is pushed.", false},
		{"Okay-ish results from the probe.", false},
	} {
		t.Run(tc.reply, func(t *testing.T) {
			if got := bareAck(tc.reply); got != tc.bare {
				t.Fatalf("bareAck = %v, want %v", got, tc.bare)
			}
			if addsNothing(tc.reply, "Something else entirely about the importer.") == "" && tc.bare {
				t.Fatalf("addsNothing let a bare acknowledgement through")
			}
		})
	}
}

func TestAFirstReplyThatOnlyAcknowledgesIsCounted(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.spawn("agent", m.Session)
	w.set("agent", func(s *Session) { s.Result, s.ResultAt = "Understood.", w.clock })
	w.loop(t, 1)
	events, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := kinds(events); len(got) != 1 || got[0] != EventIdleAgain {
		t.Fatalf("events %v, want one idle-again", got)
	}
}

func TestPastTheCapABriefIsAskedForOncePerWakeAsNewEventsArrive(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	if err := w.core.Store.SetContextCap(100_000); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.WriteBrief(m.Session, brief("start")); err != nil {
		t.Fatal(err)
	}
	asks := func() int { return countLines(w.handedTo(m.Session), "your context is at") }
	event := func(what string) {
		t.Helper()
		w.clock = w.clock.Add(30 * time.Second)
		if _, _, err := w.core.RecordGoalEvent(g.ID, EventFinished, "agent a1 is idle. Its last message, verbatim: "+what, ""); err != nil {
			t.Fatal(err)
		}
	}
	w.set(m.Session, func(s *Session) { s.Context, s.ContextAt, s.Result, s.ResultAt = 120_000, w.clock, "Looked.", w.clock })
	event("first")
	w.loop(t, 1)
	if got := asks(); got != 1 {
		t.Fatalf("%d asks after the first due wake, want 1", got)
	}
	was := m.Session
	for _, what := range []string{"second", "third", "fourth"} {
		event(what)
		w.loop(t, 1)
		if got := asks(); got != 1 {
			t.Fatalf("%d asks after %s, want still 1", got, what)
		}
		if got := wakes(w.handedTo(was)); len(got) != 0 {
			t.Fatalf("the held wake went out: %q", got)
		}
	}
	w.clock = w.clock.Add(BriefGrace)
	w.loop(t, 1)
	if o := mustOrg(t, w); o.Managers[m.ID].Session == was {
		t.Fatal("the unanswered ask never ended in a restart")
	}
}

func TestAWakeBetweenTwoEventsLetsTheCapAskAgain(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	if err := w.core.Store.SetContextCap(100_000); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.WriteBrief(m.Session, brief("start")); err != nil {
		t.Fatal(err)
	}
	w.set(m.Session, func(s *Session) { s.Context, s.ContextAt, s.Result, s.ResultAt = 120_000, w.clock, "Looked.", w.clock })
	if err := w.core.Store.noteContext(m.ID, ManagerContext{Session: m.Session, Tokens: 120_000, At: w.clock}); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.noteBriefAsk(m.ID, w.clock); err != nil {
		t.Fatal(err)
	}
	r, err := w.core.Store.digest(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(time.Hour)
	r.ToldAt = w.clock
	if err := w.core.Store.saveDigest(m.ID, r); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(time.Minute)
	if _, _, err := w.core.RecordGoalEvent(g.ID, EventCIRed, "CI failed", ""); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 1)
	if got := countLines(w.handedTo(m.Session), "your context is at"); got != 1 {
		t.Fatalf("%d asks, want one for the new wake", got)
	}
}

func TestAChiefPastTheCapIsNotAskedForABriefBecauseAReportWroteOne(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	if err := w.core.Store.SetContextCap(100_000); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.WriteBrief(chief.Session, brief("watching")); err != nil {
		t.Fatal(err)
	}
	w.clock = w.clock.Add(time.Minute)
	if _, err := w.core.WriteBrief(m.Session, brief("moved")); err != nil {
		t.Fatal(err)
	}
	w.set(chief.Session, func(s *Session) { s.Context, s.ContextAt, s.Result, s.ResultAt = 120_000, w.clock, "Looked.", w.clock })
	if _, _, err := w.core.RecordGoalEvent(g.ID, EventVerified, "attempt 1 passed", ""); err != nil {
		t.Fatal(err)
	}
	w.loop(t, 1)
	if got := w.handedTo(chief.Session); countLines(got, "your context is at") != 0 {
		t.Fatalf("the chief was asked %q", got)
	}
	if o := mustOrg(t, w); o.Managers[chief.ID].Session == chief.Session {
		t.Fatal("a chief with its own brief current was not restarted")
	}
}

func TestAChiefsOwnAgentsReplyWakesItAfterTheReplyDebounceUnlessItReadIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		read  bool
		woken bool
	}{{"left alone", false, true}, {"read first", true, false}} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Pace = nil
			_, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "platform", m.ID)
			w.clock = w.clock.Add(time.Minute)
			e, _, err := w.core.RecordGoalEvent(chief.ID, EventFinished, "agent a1 is idle. Its last message, verbatim: pushed", "")
			if err != nil {
				t.Fatal(err)
			}
			w.clock = w.clock.Add(ReplyDebounce - time.Second)
			w.loop(t, 1)
			if got := digests(w.handedTo(chief.Session)); len(got) != 0 {
				t.Fatalf("woken before the debounce: %q", got)
			}
			if tc.read {
				if err := w.core.NotePulled(chief.Session, []GoalEvent{e}); err != nil {
					t.Fatal(err)
				}
			}
			w.clock = w.clock.Add(time.Second)
			w.loop(t, 1)
			if got := digests(w.handedTo(chief.Session)); (len(got) == 1) != tc.woken {
				t.Fatalf("digests %q, want woken=%v", got, tc.woken)
			}
		})
	}
}

func TestAPendingDigestRewriteKeepsItsBriefLineAndAsksOncePerEvent(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	chief := w.chief(t, "platform", m.ID)
	w.core.BackgroundSend = func(Live, string) error { return errors.New("pane busy") }
	w.clock = w.clock.Add(time.Minute)
	verified := func(what string) {
		t.Helper()
		if _, _, err := w.core.RecordGoalEvent(g.ID, EventVerified, what, ""); err != nil {
			t.Fatal(err)
		}
		w.clock = w.clock.Add(time.Minute)
		w.loop(t, 1)
	}
	verified("attempt 1 passed")
	verified("attempt 2 passed")
	verified("attempt 3 passed")
	var text string
	if err := w.core.Store.db.QueryRow(`SELECT text FROM ym_messages WHERE session = ? AND state = ? AND text LIKE ? ORDER BY seq DESC LIMIT 1`, chief.Session, MsgPending, digestHead+"%").Scan(&text); err != nil {
		t.Fatal(err)
	}
	if strings.Count(text, briefAsk) != 1 || !strings.Contains(text, "ready") && !strings.Contains(text, "verified") {
		t.Fatalf("pending digest %q, want the news and the brief line once", text)
	}
}
