package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"time"
)

const hostSchema = `
CREATE TABLE IF NOT EXISTS ym_hosts (
	name TEXT PRIMARY KEY,
	first_used INTEGER NOT NULL,
	last_seen INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_host_boards (
	host TEXT PRIMARY KEY,
	at INTEGER NOT NULL,
	board BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_host_items (
	host TEXT PRIMARY KEY,
	at INTEGER NOT NULL,
	items BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_relayed_usage (
	host TEXT PRIMARY KEY,
	body TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_forgotten_hosts (
	host TEXT PRIMARY KEY,
	machine TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_machine (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	machine TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_launch_target (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	host TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_launch_harness (
	kind TEXT PRIMARY KEY,
	harness TEXT NOT NULL
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
	if _, err := s.db.Exec(`INSERT INTO ym_hosts(name,first_used,last_seen) VALUES(?,?,?)
		ON CONFLICT(name) DO UPDATE SET last_seen=excluded.last_seen`, host, at.UnixNano(), at.UnixNano()); err != nil {
		return err
	}
	return s.heardFrom(host, "")
}

// SeeHost marks a remembered host as answering at at; it never adds one.
func (s *Store) SeeHost(host string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE ym_hosts SET last_seen=? WHERE name=?`, at.UnixNano(), host)
	return err
}

// ForgetHost drops host and reports whether ym remembered it. What other hosts say about it stays out until it reports here itself.
func (s *Store) ForgetHost(host string) (bool, error) {
	machine, err := s.machineOfHost(host)
	if err != nil {
		return false, err
	}
	res, err := s.db.Exec(`DELETE FROM ym_hosts WHERE name=?`, host)
	if err != nil {
		return false, err
	}
	if _, err := s.db.Exec(`DELETE FROM ym_host_boards WHERE host=?`, host); err != nil {
		return false, err
	}
	if _, err := s.db.Exec(`DELETE FROM ym_host_nicknames WHERE host=?`, host); err != nil {
		return false, err
	}
	relayed, err := s.db.Exec(`DELETE FROM ym_relayed_usage WHERE host=?`, host)
	if err != nil {
		return false, err
	}
	items, err := s.db.Exec(`DELETE FROM ym_host_items WHERE host=?`, host)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	m, err := relayed.RowsAffected()
	if err != nil {
		return false, err
	}
	k, err := items.RowsAffected()
	if err != nil || n+m+k == 0 {
		return false, err
	}
	_, err = s.db.Exec(`INSERT INTO ym_forgotten_hosts(host,machine) VALUES(?,?) ON CONFLICT(host) DO UPDATE SET machine=excluded.machine`, host, machine)
	return true, err
}

// machineOfHost is the machine a stored report or item snapshot names under host, "" when none does.
func (s *Store) machineOfHost(host string) (string, error) {
	var machine string
	err := s.db.QueryRow(`SELECT COALESCE((SELECT json_extract(body,'$.machine') FROM ym_relayed_usage WHERE host=?),
		(SELECT json_extract(CAST(items AS TEXT),'$.machine') FROM ym_host_items WHERE host=?), '')`, host, host).Scan(&machine)
	return machine, err
}

// forgottenHosts are the hosts forgotten here, each with its machine when that was known.
type forgottenHosts map[string]string

// has reports whether a report under name from machine is about a forgotten host: its machine whatever it is called now, and its name
// when either side names no machine.
func (f forgottenHosts) has(name, machine string) bool {
	if kept, ok := f[name]; ok && (kept == "" || machine == "" || kept == machine) {
		return true
	}
	return machine != "" && slices.Contains(slices.Collect(maps.Values(f)), machine)
}

func (s *Store) forgottenHosts() (forgottenHosts, error) {
	rows, err := s.db.Query(`SELECT host, machine FROM ym_forgotten_hosts`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := forgottenHosts{}
	for rows.Next() {
		var host, machine string
		if err := rows.Scan(&host, &machine); err != nil {
			return nil, err
		}
		out[host] = machine
	}
	return out, rows.Err()
}

// heardFrom ends a host's forgetting: it reported here itself, or ym remote reached it.
func (s *Store) heardFrom(host, machine string) error {
	_, err := s.db.Exec(`DELETE FROM ym_forgotten_hosts WHERE host=? OR (machine<>'' AND machine=?)`, host, machine)
	return err
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

// LaunchTarget is the host the board last started a session on, "" for this one.
func (s *Store) LaunchTarget() (string, error) {
	var host string
	err := s.db.QueryRow(`SELECT host FROM ym_launch_target WHERE id=1`).Scan(&host)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return host, err
}

// SetLaunchTarget remembers host, "" for this one, as where the board starts sessions by default.
func (s *Store) SetLaunchTarget(host string) error {
	_, err := s.db.Exec(`INSERT INTO ym_launch_target(id,host) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET host=excluded.host`, host)
	return err
}

// LaunchHarness is the harness the board last started a kind of launch on, such as a goal or a chief, "claude" before any.
func (s *Store) LaunchHarness(kind string) (string, error) {
	harness := "claude"
	err := s.db.QueryRow(`SELECT harness FROM ym_launch_harness WHERE kind=?`, kind).Scan(&harness)
	if errors.Is(err, sql.ErrNoRows) {
		return harness, nil
	}
	return harness, err
}

// SetLaunchHarness remembers harness as where the board starts a kind of launch by default.
func (s *Store) SetLaunchHarness(kind, harness string) error {
	_, err := s.db.Exec(`INSERT INTO ym_launch_harness(kind,harness) VALUES(?,?) ON CONFLICT(kind) DO UPDATE SET harness=excluded.harness`, kind, harness)
	return err
}

// KeepBoard stores the board host printed at at, so a later board shows it while host cannot be reached. It replaces a kept
// board only once that is a minute old: a host answers every few seconds, and each board runs to hundreds of kilobytes.
func (s *Store) KeepBoard(host string, at time.Time, board []byte) error {
	res, err := s.db.Exec(`INSERT INTO ym_host_boards(host,at,board) VALUES(?,?,?)
		ON CONFLICT(host) DO UPDATE SET at=excluded.at, board=excluded.board WHERE excluded.at - ym_host_boards.at >= ?`,
		host, at.UnixNano(), board, int64(keptBoardEvery))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	var kept struct {
		Managers []Manager `json:"managers"`
		Goals    []Goal    `json:"goals"`
	}
	if json.Unmarshal(board, &kept) != nil {
		return nil
	}
	names := map[string]string{}
	for _, m := range kept.Managers {
		names[RemoteKey(host, m.ID)] = m.Name
	}
	for _, g := range kept.Goals {
		names[RemoteKey(host, g.ID)] = g.Name
	}
	return s.noteHostNames(names)
}

const keptBoardEvery = time.Minute

// KeptBoard is the last board host printed and when; ok is false when ym never kept one.
func (s *Store) KeptBoard(host string) (at time.Time, board []byte, ok bool, err error) {
	var n int64
	err = s.db.QueryRow(`SELECT at, board FROM ym_host_boards WHERE host=?`, host).Scan(&n, &board)
	if errors.Is(err, sql.ErrNoRows) {
		return at, nil, false, nil
	}
	return time.Unix(0, n), board, err == nil, err
}
