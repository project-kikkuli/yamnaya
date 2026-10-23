package core

import "time"

func (s *Store) RecordKill(session string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO ym_kills(session,at) VALUES(?,?)
		ON CONFLICT(session) DO UPDATE SET at=excluded.at`, session, at.UnixNano())
	return err
}

func (s *Store) Kills(since time.Time) (map[string]time.Time, error) {
	all, err := remembered(s, "kills", asIs[map[string]time.Time], func() (map[string]time.Time, error) { return s.kills(time.Time{}) })
	out := map[string]time.Time{}
	for id, at := range all {
		if !at.Before(since) {
			out[id] = at
		}
	}
	return out, err
}

func (s *Store) kills(since time.Time) (map[string]time.Time, error) {
	rows, err := s.db.Query(`SELECT session,at FROM ym_kills WHERE at>=?`, since.UnixNano())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var raw int64
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, err
		}
		out[id] = time.Unix(0, raw)
	}
	return out, rows.Err()
}

func (s *Store) ClearKill(session string) error {
	_, err := s.db.Exec(`DELETE FROM ym_kills WHERE session=?`, session)
	return err
}
