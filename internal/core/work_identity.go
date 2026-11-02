package core

import (
	"database/sql"
	"encoding/json"
	"errors"
)

type workReader interface {
	Query(string, ...any) (*sql.Rows, error)
}

func workSuccessorsIn(q workReader) (map[string]string, error) {
	rows, err := q.Query(`SELECT host,session,successor FROM ym_work_successors`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var host, session, next string
		if err = rows.Scan(&host, &session, &next); err != nil {
			return nil, err
		}
		out[RemoteKey(host, session)] = RemoteKey(host, next)
	}
	return out, rows.Err()
}

func saveWorkSuccessorsIn(tx *sql.Tx, host string, successors map[string]string) error {
	for session, next := range successors {
		_, _, fromRemote := SplitRemote(session)
		_, _, toRemote := SplitRemote(next)
		if host == "" || session == "" || next == "" || session == next || fromRemote || toRemote {
			return errors.New("invalid worker continuation identity")
		}
		if _, err := tx.Exec(`INSERT INTO ym_work_successors(host,session,successor) VALUES (?,?,?) ON CONFLICT(host,session) DO UPDATE SET successor=excluded.successor`, host, session, next); err != nil {
			return err
		}
	}
	return nil
}

func workIdentity(self, session string, successors map[string]string) string {
	host, raw, remote := SplitRemote(session)
	if !remote {
		host, raw = self, session
	}
	at := RemoteKey(host, raw)
	seen := map[string]bool{}
	for successors[at] != "" && !seen[at] {
		seen[at] = true
		at = successors[at]
	}
	return at
}

func (c *Core) currentWorkIdentity(self, session string, successors map[string]string) string {
	host, raw, remote := SplitRemote(session)
	if !remote {
		host, raw = self, session
	}
	if host == self {
		raw = c.Resolve(raw)
	}
	return workIdentity(self, RemoteKey(host, raw), successors)
}

func assignmentOwnersIn(q workReader, identity func(string) string) (map[string]string, error) {
	rows, err := q.Query(`SELECT id,session FROM ym_assignments ORDER BY generation DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	owners := map[string]string{}
	for rows.Next() {
		var id, session string
		if err = rows.Scan(&id, &session); err != nil {
			return nil, err
		}
		key := identity(session)
		if owners[key] == "" {
			owners[key] = id
		}
	}
	return owners, rows.Err()
}

func (c *Core) remoteWorkSuccessorsIn(tx *sql.Tx, self, origin, worker string) (map[string]string, bool, error) {
	saved, err := workSuccessorsIn(tx)
	if err != nil {
		return nil, false, err
	}
	rows, err := tx.Query(`SELECT body,session,bound_session FROM ym_remote_work WHERE origin=?`, origin)
	if err != nil {
		return nil, false, err
	}
	roots := map[string]bool{worker: true}
	for rows.Next() {
		var body, session, bound string
		var w RemoteAssignment
		if err = rows.Scan(&body, &session, &bound); err == nil {
			err = json.Unmarshal([]byte(body), &w)
		}
		if err != nil {
			rows.Close()
			return nil, false, err
		}
		if w.Host == self {
			for _, id := range []string{w.Worker, session, bound} {
				if id != "" {
					roots[id] = true
				}
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, false, err
	}
	target := c.currentWorkIdentity(self, worker, saved)
	out, changed := map[string]string{}, false
	for root := range roots {
		path, seen, at := map[string]string{}, map[string]bool{}, root
		for !seen[at] {
			seen[at] = true
			next := c.successor(at, "")
			if next == "" {
				_, next, _ = SplitRemote(saved[RemoteKey(self, at)])
			}
			if next == "" {
				break
			}
			if seen[next] {
				path = nil
				break
			}
			path[at], at = next, next
		}
		if RemoteKey(self, at) != target {
			continue
		}
		for from, to := range path {
			out[from] = to
			changed = changed || saved[RemoteKey(self, from)] != RemoteKey(self, to)
		}
	}
	return out, changed, nil
}
