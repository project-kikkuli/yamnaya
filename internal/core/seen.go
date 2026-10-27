package core

import "time"

const seenSchema = `
CREATE TABLE IF NOT EXISTS ym_session_seen (
	session TEXT PRIMARY KEY,
	at TEXT NOT NULL
) WITHOUT ROWID;
`

// MarkOpened records that the owner opened session at, so what it said until then reads as read.
func (s *Store) MarkOpened(session string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO ym_session_seen (session, at) VALUES (?, ?) ON CONFLICT(session) DO UPDATE SET at = excluded.at`, session, stamp(at))
	return err
}

// Opened is when the owner last opened each session.
func (s *Store) Opened() (map[string]time.Time, error) {
	return remembered(s, "opened", cloneMap[string, time.Time], s.opened)
}

func (s *Store) opened() (map[string]time.Time, error) {
	rows, err := s.db.Query(`SELECT session, at FROM ym_session_seen`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var session, at string
		if err := rows.Scan(&session, &at); err != nil {
			return nil, err
		}
		out[session], _ = time.Parse(time.RFC3339Nano, at)
	}
	return out, rows.Err()
}
