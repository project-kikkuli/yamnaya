package core

import "time"

const hostSchema = `
CREATE TABLE IF NOT EXISTS ym_hosts (
	name TEXT PRIMARY KEY,
	first_used INTEGER NOT NULL,
	last_seen INTEGER NOT NULL
);
`

// KnownHost is a host ym remote reached, which the board reads beside this one.
type KnownHost struct {
	Name      string
	FirstUsed time.Time
	LastSeen  time.Time
}

// RecordHost remembers host, first used now when ym has not reached it before.
func (s *Store) RecordHost(host string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO ym_hosts(name,first_used,last_seen) VALUES(?,?,?)
		ON CONFLICT(name) DO UPDATE SET last_seen=excluded.last_seen`, host, at.UnixNano(), at.UnixNano())
	return err
}

// SeeHost marks a remembered host as answering at at; it never adds one.
func (s *Store) SeeHost(host string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE ym_hosts SET last_seen=? WHERE name=?`, at.UnixNano(), host)
	return err
}

// ForgetHost drops host and reports whether ym remembered it.
func (s *Store) ForgetHost(host string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM ym_hosts WHERE name=?`, host)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// KnownHosts lists the remembered hosts, first used first.
func (s *Store) KnownHosts() ([]KnownHost, error) {
	rows, err := s.db.Query(`SELECT name,first_used,last_seen FROM ym_hosts ORDER BY first_used, name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnownHost
	for rows.Next() {
		var h KnownHost
		var first, last int64
		if err := rows.Scan(&h.Name, &first, &last); err != nil {
			return nil, err
		}
		h.FirstUsed, h.LastSeen = time.Unix(0, first), time.Unix(0, last)
		out = append(out, h)
	}
	return out, rows.Err()
}
