package handoff

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

type Store struct{ db *sql.DB }

func New(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS ym_handoff_files(path TEXT PRIMARY KEY,identity TEXT NOT NULL,offset INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS ym_handoff_events(id TEXT PRIMARY KEY,work TEXT NOT NULL,event BLOB NOT NULL,state TEXT NOT NULL,error TEXT NOT NULL DEFAULT '');
 CREATE INDEX IF NOT EXISTS ym_handoff_pending ON ym_handoff_events(state,work);
 CREATE TABLE IF NOT EXISTS ym_handoff_claims(id TEXT PRIMARY KEY,work TEXT NOT NULL,claim BLOB NOT NULL,state TEXT NOT NULL DEFAULT 'pending',error TEXT NOT NULL DEFAULT '');
 CREATE TABLE IF NOT EXISTS ym_handoff_items(id TEXT PRIMARY KEY,item INTEGER NOT NULL);
 CREATE TABLE IF NOT EXISTS ym_handoff_targets(id TEXT PRIMARY KEY,target TEXT NOT NULL);`)
	return &Store{db: db}, err
}

func (s *Store) Cursor(path, identity string) (int64, error) {
	var offset int64
	err := s.db.QueryRow(`SELECT offset FROM ym_handoff_files WHERE path=? AND identity=?`, path, identity).Scan(&offset)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	return offset, err
}

func (s *Store) Ingest(path, identity string, offset int64, events []Event) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		state := "pending"
		if e.Error != "" {
			state = "error"
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_handoff_events(id,work,event,state,error) VALUES(?,?,?,?,?)`, e.ID, e.Work, raw, state, e.Error); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO ym_handoff_files(path,identity,offset) VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET identity=excluded.identity,offset=excluded.offset`, path, identity, offset); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Batch() ([]Event, error) {
	rows, err := s.db.Query(`SELECT event FROM ym_handoff_events WHERE state='pending' AND work=(SELECT work FROM ym_handoff_events WHERE state='pending' ORDER BY json_extract(CAST(event AS TEXT),'$.At') DESC,rowid DESC LIMIT 1) ORDER BY json_extract(CAST(event AS TEXT),'$.At') DESC,rowid DESC LIMIT 16`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	size := 0
	for rows.Next() {
		var raw []byte
		var e Event
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &e); err != nil {
			return nil, err
		}
		if len(out) > 0 && size+len(e.Text) > 65536 {
			break
		}
		out = append(out, e)
		size += len(e.Text)
	}
	return out, rows.Err()
}

func (s *Store) Classified(events []Event, claims []Claim) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, c := range claims {
		raw, err := json.Marshal(c)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_handoff_claims(id,work,claim) VALUES(?,?,?)`, c.ID, c.Event.Work, raw); err != nil {
			return err
		}
	}
	for _, e := range events {
		if _, err = tx.Exec(`UPDATE ym_handoff_events SET state='classified',error='' WHERE id=?`, e.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) Fail(events []Event, cause error) error {
	for _, e := range events {
		if _, err := s.db.Exec(`UPDATE ym_handoff_events SET state='error',error=? WHERE id=?`, cause.Error(), e.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Claims(workID string, pending bool) ([]Claim, error) {
	query := `SELECT claim FROM ym_handoff_claims WHERE (?='' OR work=?)`
	if pending {
		query += ` AND state='pending'`
	}
	rows, err := s.db.Query(query, workID, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Claim
	for rows.Next() {
		var raw []byte
		var c Claim
		if err = rows.Scan(&raw); err != nil {
			return nil, err
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Applied(id, target string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO ym_handoff_targets(id,target) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET target=excluded.target`, id, target); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE ym_handoff_claims SET state='applied',error='' WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) Issue(id string, cause error) error {
	_, err := s.db.Exec(`UPDATE ym_handoff_claims SET state='blocked',error=? WHERE id=?`, cause.Error(), id)
	return err
}

func (s *Store) Skip(id string) error {
	_, err := s.db.Exec(`UPDATE ym_handoff_claims SET state='archived',error='' WHERE id=?`, id)
	return err
}

func (s *Store) Resume() error {
	_, err := s.db.Exec(`UPDATE ym_handoff_events SET state='pending',error='' WHERE state='error' AND json_extract(CAST(event AS TEXT),'$.Error')=''; UPDATE ym_handoff_claims SET state='pending',error='' WHERE state='blocked'`)
	return err
}

func (s *Store) Status() (map[string]int, error) {
	rows, err := s.db.Query(`SELECT 'events/'||state,count(*) FROM ym_handoff_events GROUP BY state UNION ALL SELECT 'claims/'||state,count(*) FROM ym_handoff_claims GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var key string
		var n int
		if err = rows.Scan(&key, &n); err != nil {
			return nil, err
		}
		out[key] = n
	}
	return out, rows.Err()
}

func (s *Store) Issues(workID string) ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT error FROM ym_handoff_events WHERE work=? AND error<>'' UNION SELECT DISTINCT error FROM ym_handoff_claims WHERE work=? AND error<>''`, workID, workID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var issue string
		if err = rows.Scan(&issue); err != nil {
			return nil, err
		}
		out = append(out, fmt.Sprintf("Handoff capture: %s", issue))
	}
	return out, rows.Err()
}

func (s *Store) Target(id string) (string, error) {
	var target string
	err := s.db.QueryRow(`SELECT target FROM ym_handoff_targets WHERE id=?`, id).Scan(&target)
	return target, err
}
