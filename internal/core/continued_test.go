package core

import (
	"path/filepath"
	"testing"
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
