package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"
)

func TestAContinuedSessionCarriesWhatWaitsOnItsOldID(t *testing.T) {
	const old, next = "11111111-1111-4111-8111-111111111111", "01a1b2c3-0000-7000-8000-000000000042"
	for _, c := range []struct {
		name string
		seed string
		args []any
		// read returns the row's session after the move, want what it must be.
		read string
		want string
	}{
		{name: "outstanding assignment", seed: `INSERT INTO ym_assignments (id, goal, session, generation, source, reason, created_at) VALUES ('a', 'g', ?, 1, 's', 'r', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_assignments WHERE id = 'a'`, want: next},
		{name: "accepted assignment keeps who did it", seed: `INSERT INTO ym_assignments (id, goal, session, generation, source, reason, state, created_at) VALUES ('a', 'g', ?, 1, 's', 'r', 'accepted', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_assignments WHERE id = 'a'`, want: old},
		{name: "running verifier", seed: `INSERT INTO ym_verifier_sessions (session, verification, at) VALUES (?, 1, 'now')`, args: []any{old},
			read: `SELECT session FROM ym_verifier_sessions WHERE verification = 1`, want: next},
		{name: "remote work session", seed: `INSERT INTO ym_remote_work (id, origin, goal, source, session, bound_session, state, body) VALUES ('w', 'o', 'g', 's', ?, ?, 'approved', '{}')`, args: []any{old, old},
			read: `SELECT session FROM ym_remote_work WHERE id = 'w'`, want: next},
		{name: "remote work binding", seed: `INSERT INTO ym_remote_work (id, origin, goal, source, session, bound_session, state, body) VALUES ('w', 'o', 'g', 's', ?, ?, 'approved', '{}')`, args: []any{old, old},
			read: `SELECT bound_session FROM ym_remote_work WHERE id = 'w'`, want: next},
		{name: "ym off follows", seed: `INSERT INTO ym_off (target, at) VALUES (?, 'now')`, args: []any{old},
			read: `SELECT target FROM ym_off`, want: next},
		{name: "vibe follows", seed: `INSERT INTO ym_vibes (place, why, session, harness, target, at) VALUES ('p', 'w', ?, 'claude', '', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_vibes`, want: next},
		{name: "last report moves", seed: `INSERT INTO ym_agent_reports (session, head, said, at) VALUES (?, 'h', 's', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_agent_reports`, want: next},
		{name: "project name follows", seed: `INSERT INTO ym_names (project, name) VALUES (?, 'renamed')`, args: []any{old},
			read: `SELECT project FROM ym_names`, want: next},
		{name: "merge from it follows", seed: `INSERT INTO ym_merges (project, into_project) VALUES (?, 'other')`, args: []any{old},
			read: `SELECT project FROM ym_merges`, want: next},
		{name: "merge into it follows", seed: `INSERT INTO ym_merges (project, into_project) VALUES ('other', ?)`, args: []any{old},
			read: `SELECT into_project FROM ym_merges`, want: next},
		{name: "waiting ask follows", seed: `INSERT INTO ym_session_asks (session, text, at) VALUES (?, 't', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_session_asks`, want: next},
		{name: "dismissed reply follows", seed: `INSERT INTO ym_waiting_dismissed (session, reply, at) VALUES (?, 'r', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_waiting_dismissed`, want: next},
		{name: "opened mark follows", seed: `INSERT INTO ym_session_seen (session, at) VALUES (?, 'now')`, args: []any{old},
			read: `SELECT session FROM ym_session_seen`, want: next},
		{name: "phases follow", seed: `INSERT INTO ym_phases (project, phases, idx) VALUES (?, '[]', 0)`, args: []any{old},
			read: `SELECT project FROM ym_phases`, want: next},
		{name: "delivered message keeps its session", seed: `INSERT INTO ym_messages (session, text, state, created_at) VALUES (?, 'x', 'sent', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_messages WHERE text = 'x'`, want: old},
		{name: "pending message moves", seed: `INSERT INTO ym_messages (session, text, state, created_at) VALUES (?, 'x', 'pending', 'now')`, args: []any{old},
			read: `SELECT session FROM ym_messages WHERE text = 'x'`, want: next},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err = s.db.Exec(c.seed, c.args...); err != nil {
				t.Fatal(err)
			}
			if err = s.ContinueSession(old, next, "codex", "default", "/root"); err != nil {
				t.Fatal(err)
			}
			var got string
			if err = s.db.QueryRow(c.read).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Fatalf("row holds %q, want %q", got, c.want)
			}
		})
	}
}

type portingResumes struct {
	*resumes
	to string
}

func (p portingResumes) Resume(session, _, _ string) (string, bool, error) {
	return p.to, true, p.w.core.Store.ContinueSession(session, p.to, "codex", "codex-main", "/codex")
}

func TestAResumedManagerKeepsTheHarnessAndLoginItsSessionMovedTo(t *testing.T) {
	const next = "01a1b2c3-0000-7000-8000-000000000042"
	for _, c := range []struct {
		name string
		// save is what ym saves from the copy it read before the move.
		save func(*testing.T, *orgWorld, Org, Manager) Manager
	}{
		{"resumed by supervise", func(t *testing.T, w *orgWorld, o Org, m Manager) Manager {
			got, ok := w.core.resumeManager(o, m, nil, nil, "")
			if !ok {
				t.Fatal("not resumed")
			}
			return got
		}},
		{"saved from a stale copy", func(t *testing.T, w *orgWorld, o Org, m Manager) Manager {
			if err := w.core.Store.ContinueSession(m.Session, next, "codex", "codex-main", "/codex"); err != nil {
				t.Fatal(err)
			}
			if err := w.core.Store.SaveManager(m); err != nil {
				t.Fatal(err)
			}
			got, err := w.core.Store.Manager(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			return got
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			w.core.Launches = portingResumes{r, next}
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			got := c.save(t, w, o, m)
			if got.Session != next || got.Harness != "codex" || got.Profile != "codex-main" || got.ConfigRoot != "/codex" {
				t.Fatalf("manager %+v", got)
			}
			saved, err := w.core.Store.Manager(m.ID)
			if err != nil || saved.Session != next || saved.Harness != "codex" {
				t.Fatalf("saved manager %+v, %v", saved, err)
			}
		})
	}
}

func TestASessionRefusedForItsLoginIsAskedAgainWithoutCostingPaceOrFailures(t *testing.T) {
	wait := func(session string) error {
		return fmt.Errorf("resume %s: %w", session, &QuotaWait{Harness: "claude", Login: "default", Key: session, Until: time.Now().Add(time.Hour)})
	}
	for _, c := range []struct {
		name    string
		manager bool
	}{
		{name: "goal agent"},
		{name: "manager", manager: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.supervise(t)
			refused := "agent-refused"
			if c.manager {
				refused = m.Session
			} else {
				r.state(m.Session, "running")
				r.launched = append(r.launched, Launched{Key: "wt-refused", Session: refused, State: "gone", Parent: m.Session}, Launched{Key: "wt-ok", Session: "agent-ok", State: "gone", Parent: m.Session})
				r.fail[refused] = wait(refused)
			}
			if c.manager {
				w.restart(r)
				r.fail[refused] = wait(refused)
			}
			w.clock = w.clock.Add(StartGrace + time.Minute)
			for range 30 {
				w.clock = w.clock.Add(5 * time.Second)
				w.loop(t, 1)
			}
			resumedCalls := func(session string) int {
				n := 0
				for _, call := range r.resumed {
					if call.session == session {
						n++
					}
				}
				return n
			}
			if resumedCalls(refused) != 0 {
				t.Fatalf("a refused session resumed: %+v", r.resumed)
			}
			if !c.manager && resumedCalls("agent-ok") != 1 {
				t.Fatalf("a refused session starved another's resume: %+v", r.resumed)
			}
			saved, err := w.core.Store.Manager(m.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Stuck() {
				t.Fatalf("waiting on a login counted as failing to launch: %+v", saved)
			}
			goal, err := w.core.Store.Goal(g.ID)
			if err != nil || goal.State == GoalStuck {
				t.Fatalf("goal marked stuck while waiting on a login: %+v, %v", goal, err)
			}
			delete(r.fail, refused)
			w.clock = w.clock.Add(QuotaRecheck + time.Second)
			for range 6 {
				w.clock = w.clock.Add(ResumePace)
				w.loop(t, 1)
			}
			if resumedCalls(refused) != 1 {
				t.Fatalf("a session refused before the reset never came back: %+v", r.resumed)
			}
		})
	}
}

func TestAStaleSaveLeavesAManagerThatStartedFreshOnItsNewSession(t *testing.T) {
	w, _ := resumeWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	stale := m
	st := w.core.Store
	if err := st.ContinueSession(m.Session, "ported", "codex", "codex-main", "/codex"); err != nil {
		t.Fatal(err)
	}
	fresh, err := st.Manager(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Session, fresh.Harness, fresh.Profile = "started-fresh", "claude", "claude-work"
	if err = st.SaveManager(fresh); err != nil {
		t.Fatal(err)
	}
	stale.Nudges = 3
	if err = st.SaveManager(stale); err != nil {
		t.Fatalf("a stale save failed: %v", err)
	}
	got, err := st.Manager(m.ID)
	if err != nil || got.Session != "started-fresh" || got.Profile != "claude-work" || got.Nudges != 3 {
		t.Fatalf("manager %+v, %v", got, err)
	}
}

func TestEveryRefusedSessionComesBackOnceAfterTheReset(t *testing.T) {
	for _, n := range []int{1, 5} {
		t.Run(fmt.Sprintf("%d refused", n), func(t *testing.T) {
			w, r := resumeWorld(t)
			_, m := w.goal(t, "ship the importer", "/repo/august")
			w.supervise(t)
			r.state(m.Session, "running")
			for i := range n {
				id := fmt.Sprintf("agent-%d", i)
				r.launched = append(r.launched, Launched{Key: "wt-" + id, Session: id, State: "gone", Parent: m.Session})
				r.fail[id] = &QuotaWait{Harness: "claude", Login: "default", Key: id, Until: w.clock.Add(time.Hour)}
			}
			for range 40 {
				w.clock = w.clock.Add(ResumePace)
				if err := w.core.ReviveWork(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			if len(r.resumed) != 0 {
				t.Fatalf("resumed before the reset: %+v", r.resumed)
			}
			for i := range n {
				delete(r.fail, fmt.Sprintf("agent-%d", i))
			}
			for range 120 {
				w.clock = w.clock.Add(ResumePace)
				if err := w.core.ReviveWork(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			counts := map[string]int{}
			for _, call := range r.resumed {
				counts[call.session]++
			}
			if len(counts) != n {
				t.Fatalf("%d of %d came back: %v", len(counts), n, counts)
			}
			for id, got := range counts {
				if got != 1 {
					t.Fatalf("%s resumed %d times", id, got)
				}
			}
		})
	}
}

func TestAFailedResumeComesBackWithoutARestart(t *testing.T) {
	w, r := resumeWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	w.supervise(t)
	r.state(m.Session, "running")
	r.launched = append(r.launched, Launched{Key: "wt-a", Session: "agent-a", State: "gone", Parent: m.Session})
	r.fail["agent-a"] = errors.New("native login required")
	w.clock = w.clock.Add(ResumePace)
	if err := w.core.ReviveWork(w.board(t)); err != nil {
		t.Fatal(err)
	}
	delete(r.fail, "agent-a")
	w.clock = w.clock.Add(ResumeRetry - ResumePace)
	if err := w.core.ReviveWork(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if len(r.resumed) != 0 {
		t.Fatalf("retried at once: %+v", r.resumed)
	}
	w.clock = w.clock.Add(2 * ResumePace)
	if err := w.core.ReviveWork(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if len(r.resumed) != 1 || r.resumed[0].session != "agent-a" {
		t.Fatalf("resumed %+v", r.resumed)
	}
}

func TestARefusalInANewWindowIsANewEvent(t *testing.T) {
	w, r := resumeWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.supervise(t)
	r.state(m.Session, "running")
	r.launched = append(r.launched, Launched{Key: "wt-a", Session: "agent-a", State: "gone", Parent: m.Session})
	first := w.clock.Add(time.Hour).Truncate(time.Hour).Add(30 * time.Minute)
	refuse := func(until time.Time) int {
		t.Helper()
		r.fail["agent-a"] = &QuotaWait{Harness: "claude", Login: "default", Key: "wt-a", Until: until}
		w.clock = w.clock.Add(QuotaRecheck + ResumePace)
		if err := w.core.ReviveWork(w.board(t)); err != nil {
			t.Fatal(err)
		}
		events, err := w.core.Store.GoalEvents(g.ID)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range events {
			if e.Kind == EventResumeFailed {
				n++
			}
		}
		return n
	}
	if got := refuse(first); got != 1 {
		t.Fatalf("%d events", got)
	}
	if got := refuse(first.Add(time.Second)); got != 1 {
		t.Fatalf("a second of jitter made %d events", got)
	}
	if got := refuse(first.Add(7 * 24 * time.Hour)); got != 2 {
		t.Fatalf("a new window made %d events, want 2", got)
	}
}

func TestAMovedResetMakesOneResumeFailedEvent(t *testing.T) {
	w, r := resumeWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.supervise(t)
	r.state(m.Session, "running")
	r.launched = append(r.launched, Launched{Key: "wt-a", Session: "agent-a", State: "gone", Parent: m.Session})
	until := w.clock.Add(time.Hour)
	for i := range 4 {
		r.fail["agent-a"] = &QuotaWait{Harness: "claude", Login: "default", Key: "wt-a", Until: until.Add(time.Duration(i) * time.Second)}
		w.clock = w.clock.Add(QuotaRecheck + ResumePace)
		if err := w.core.ReviveWork(w.board(t)); err != nil {
			t.Fatal(err)
		}
	}
	events, err := w.core.Store.GoalEvents(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, e := range events {
		if e.Kind == EventResumeFailed {
			failed++
		}
	}
	if failed != 1 {
		t.Fatalf("%d resume-failed events, want 1", failed)
	}
}

func TestASwitchStoppingASessionDoesNotReadAsItsExit(t *testing.T) {
	for _, c := range []struct {
		name      string
		switching *Switching
		told      bool
	}{
		{"an ordinary exit is reported", nil, true},
		{"a crash while a switch only waits is reported", &Switching{To: "claude-work"}, true},
		{"the stop a switch made itself is not", &Switching{To: "claude-work", Stopped: true}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, r := resumeWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.supervise(t)
			r.state(m.Session, "running")
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			if err = w.core.Store.Link("agent-a", m.Session); err != nil {
				t.Fatal(err)
			}
			b := w.board(t)
			b.Launched = append(b.Launched, Launched{Key: "wt-a", Session: "agent-a", State: "exited 143", Parent: m.Session, Path: "/wt/a", Switching: c.switching})
			if err = w.core.noticeExits(b, o); err != nil {
				t.Fatal(err)
			}
			events, err := w.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			told := false
			for _, e := range events {
				told = told || e.Kind == EventAgentStopped
			}
			if told != c.told {
				t.Fatalf("told=%v, want %v: %+v", told, c.told, events)
			}
		})
	}
}

func TestSessionsThatKeepFailingNeverStarveOneThatWouldComeBack(t *testing.T) {
	w, r := resumeWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	w.supervise(t)
	r.state(m.Session, "running")
	for i := range 40 {
		id := fmt.Sprintf("failing-%d", i)
		r.launched = append(r.launched, Launched{Key: "wt-" + id, Session: id, State: "gone", Parent: m.Session})
		r.fail[id] = errors.New("native login required")
	}
	r.launched = append(r.launched, Launched{Key: "wt-ok", Session: "healthy", State: "gone", Parent: m.Session})
	for range 200 {
		w.clock = w.clock.Add(ResumePace)
		if err := w.core.ReviveWork(w.board(t)); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.resumed) != 1 || r.resumed[0].session != "healthy" {
		t.Fatalf("resumed %+v", r.resumed)
	}
}
