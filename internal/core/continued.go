package core

import (
	"database/sql"
	"errors"
)

const continuedSchema = `
CREATE TABLE IF NOT EXISTS ym_continued (
	session TEXT PRIMARY KEY,
	successor TEXT NOT NULL
) WITHOUT ROWID;
`

// Continued maps a session ported to another harness to the session that carries its conversation now.
func (s *Store) Continued() (map[string]string, error) {
	return remembered(s, "continued", cloneMap[string, string], func() (map[string]string, error) {
		rows, err := s.db.Query(`SELECT session, successor FROM ym_continued`)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var old, next string
			if err = rows.Scan(&old, &next); err != nil {
				return nil, err
			}
			out[old] = next
		}
		return out, rows.Err()
	})
}

// ContinueSession records that session old now runs as next on harness and profile, and moves what waits on old to next. Items, kills
// and reports keep the id they were filed under; Resolve carries those to next.
func (s *Store) ContinueSession(old, next, harness, profile, root string) error {
	defer s.invalidate()
	if old == "" || next == "" || old == next {
		return errors.New("a continued session needs two different ids")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := now()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ym_continued (session, successor) VALUES (?, ?) ON CONFLICT(session) DO UPDATE SET successor = excluded.successor`, []any{old, next}},
		{`INSERT OR IGNORE INTO ym_manager_sessions (session, manager, at) SELECT ?, manager, ? FROM ym_manager_sessions WHERE session = ?`, []any{next, at, old}},
		{`UPDATE ym_managers SET session = ?, harness = ?, profile = ?, config_root = ? WHERE session = ?`, []any{next, harness, profile, root, old}},
		{`UPDATE ym_messages SET session = ? WHERE session = ? AND state = ?`, []any{next, old, MsgPending}},
		{`INSERT INTO ym_links (child, parent) SELECT ?, parent FROM ym_links WHERE child = ? ON CONFLICT(child) DO UPDATE SET parent = excluded.parent`, []any{next, old}},
		{`UPDATE ym_links SET parent = ? WHERE parent = ?`, []any{next, old}},
		{`UPDATE ym_assignments SET session = ? WHERE session = ? AND state = 'outstanding'`, []any{next, old}},
		{`UPDATE ym_goal_verifications SET verifier_session = ? WHERE verifier_session = ?`, []any{next, old}},
		{`UPDATE ym_verifier_sessions SET session = ? WHERE session = ?`, []any{next, old}},
		{`INSERT OR IGNORE INTO ym_remote_work_aliases (session, assignment) SELECT ?, assignment FROM ym_remote_work_aliases WHERE session = ?`, []any{next, old}},
		{`UPDATE ym_remote_work SET session = ? WHERE session = ?`, []any{next, old}},
		{`UPDATE ym_remote_work SET bound_session = ? WHERE bound_session = ?`, []any{next, old}},
		{`INSERT OR IGNORE INTO ym_off (target, at) SELECT ?, at FROM ym_off WHERE target = ?`, []any{next, old}},
		{`DELETE FROM ym_off WHERE target = ?`, []any{old}},
		{`UPDATE ym_vibes SET session = ? WHERE session = ?`, []any{next, old}},
		{`UPDATE ym_vibes SET target = ? WHERE target = ?`, []any{next, old}},
		{`UPDATE OR REPLACE ym_agent_reports SET session = ? WHERE session = ?`, []any{next, old}},
		{`UPDATE OR IGNORE ym_phases SET project = ? WHERE project = ?`, []any{next, old}},
		{`UPDATE OR IGNORE ym_names SET project = ? WHERE project = ?`, []any{next, old}},
		{`UPDATE OR IGNORE ym_merges SET project = ? WHERE project = ?`, []any{next, old}},
		{`UPDATE ym_merges SET into_project = ? WHERE into_project = ?`, []any{next, old}},
		{`UPDATE OR REPLACE ym_session_asks SET session = ? WHERE session = ?`, []any{next, old}},
		{`UPDATE OR IGNORE ym_waiting_dismissed SET session = ? WHERE session = ?`, []any{next, old}},
		{`UPDATE OR REPLACE ym_session_seen SET session = ? WHERE session = ?`, []any{next, old}},
	} {
		if _, err = tx.Exec(q.sql, q.args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// currentManagerSession keeps a save from a copy read before its session moved from undoing the move: what the manager runs as now,
// which may since have started fresh, stays as the row has it.
func currentManagerSession(tx *sql.Tx, m *Manager, num int64) error {
	var next string
	err := tx.QueryRow(`SELECT successor FROM ym_continued WHERE session = ?`, m.Session).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return tx.QueryRow(`SELECT session, harness, profile, config_root FROM ym_managers WHERE num = ?`, num).Scan(&m.Session, &m.Harness, &m.Profile, &m.ConfigRoot)
}

// RetireItem dismisses an item whose question has answered itself, without telling the session that happened to file it.
func (s *Store) RetireItem(ref, reason string) error {
	it, err := s.Transition(ref, ItemDismissed, reason)
	if err != nil {
		return err
	}
	defer s.invalidate()
	_, err = s.db.Exec(`UPDATE ym_items SET delivered_at = ? WHERE seq = ?`, now(), it.Seq)
	return err
}
