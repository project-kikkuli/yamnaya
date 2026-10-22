package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"
)

const (
	KindDecision = "decision"
	KindDefault  = "default"
	KindChore    = "chore"
	// KindGo asks the owner's go-ahead to build; answering it sends GoAhead.
	KindGo = "go"

	GoAhead = "PLEASE GO IMPLEMENT THIS"

	ItemOpen      = "open"
	ItemAnswered  = "answered"
	ItemDismissed = "dismissed"
	ItemSnoozed   = "snoozed"
)

var kindPrefix = map[string]string{KindDecision: "D", KindDefault: "V", KindChore: "C", KindGo: "G"}

type Option struct {
	Label  string `json:"label"`
	Effect string `json:"effect"`
}

// Item is a question an agent filed for the owner. IDs are numbered per scope, one sequence across kinds, and never reused.
type Item struct {
	// Seq is the item's number across every scope; Key() names the item uniquely with it.
	Seq int64
	// Scope is the repository identity of the filing session: its git common dir, else its directory.
	Scope string
	ID    string
	// Alias is the ID the item had before IDs were numbered per scope.
	Alias      string
	Kind       string
	Action     string
	Question   string
	Rec        string
	Options    []Option
	Assumption string
	Blocks     int
	Session    string
	Harness    string
	CreatedAt  time.Time
	State      string
	Answer     string
	Reason     string
	AnsweredAt time.Time
	// Delivered is when the agent received the answer or dismissal.
	Delivered time.Time
	// Held is when the agent's session took the answer but held it for the owner's approval.
	Held time.Time
	// Recipient is who the item waits on: Owner, or the id of the manager or chief it was routed to.
	Recipient string
	History   []Event
	// Host is the host whose ym holds the item when that is not this one.
	Host string
}

type Event struct {
	At   time.Time
	From string
	To   string
	Text string
}

// Store keeps items, phases, vibes, lineage links, project names and merges.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS ym_items (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	scope TEXT NOT NULL DEFAULT '',
	num INTEGER,
	id TEXT NOT NULL,
	alias TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL,
	question TEXT NOT NULL,
	rec TEXT NOT NULL DEFAULT '',
	options TEXT NOT NULL DEFAULT '[]',
	assumption TEXT NOT NULL DEFAULT '',
	blocks INTEGER NOT NULL DEFAULT 0,
	session TEXT NOT NULL,
	harness TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	state TEXT NOT NULL,
	answer TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	answered_at TEXT,
	delivered_at TEXT
);
CREATE TABLE IF NOT EXISTS ym_item_events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	item TEXT NOT NULL,
	at TEXT NOT NULL,
	from_state TEXT NOT NULL,
	to_state TEXT NOT NULL,
	text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ym_item_events_item ON ym_item_events(item);
CREATE TABLE IF NOT EXISTS ym_phases (
	project TEXT PRIMARY KEY,
	phases TEXT NOT NULL,
	idx INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS ym_links (
	child TEXT PRIMARY KEY,
	parent TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_names (
	project TEXT PRIMARY KEY,
	name TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_merges (
	project TEXT PRIMARY KEY,
	into_project TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_repos (
	path TEXT PRIMARY KEY,
	picked_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_kills (
	session TEXT PRIMARY KEY,
	at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_scans (
	path TEXT PRIMARY KEY,
	offset INTEGER NOT NULL,
	data BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_prs (
	url TEXT PRIMARY KEY,
	state TEXT NOT NULL DEFAULT '',
	title TEXT NOT NULL DEFAULT '',
	review TEXT NOT NULL DEFAULT '',
	passed INTEGER NOT NULL DEFAULT 0,
	failed INTEGER NOT NULL DEFAULT 0,
	pending INTEGER NOT NULL DEFAULT 0,
	head TEXT NOT NULL DEFAULT '',
	commits INTEGER NOT NULL DEFAULT 0,
	threads INTEGER NOT NULL DEFAULT 0,
	unresolved INTEGER NOT NULL DEFAULT 0,
	fetched_at TEXT NOT NULL
);
`

func DefaultStorePath() (string, error) {
	if dir := os.Getenv("YAMNAYA_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "ym.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "yamnaya", "ym.db"), nil
}

// OpenStore opens the store, one process at a time: SQLite fails a switch to WAL as busy, without waiting, while another process opens the file.
func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(path+".open.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema + messageSchema + waitingSchema + forwardSchema + orgSchema + verifySchema + hostSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := scopeItems(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate items: %w", err)
	}
	if err := prThreads(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate prs: %w", err)
	}
	if err := prMergeDetails(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate merge facts: %w", err)
	}
	if err := vibes(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate vibes: %w", err)
	}
	if err := heldItems(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate held items: %w", err)
	}
	if err := itemActions(db); err != nil {
		db.Close()
		return nil, err
	}
	if err := itemRecipients(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate item recipients: %w", err)
	}
	if err := retireWork(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("retire work layer: %w", err)
	}
	if err := sessionAsks(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate session asks: %w", err)
	}
	if err := retireHandoffs(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("retire handoff capture: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

func parseTime(s sql.NullString) time.Time {
	if !s.Valid {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s.String)
	return t
}

// Phases are a project's ordered steps and the index of the current one.
type Phases struct {
	Names   []string
	Current int
}

func (s *Store) Phases(project string) (Phases, error) {
	var p Phases
	var raw string
	err := s.db.QueryRow(`SELECT phases, idx FROM ym_phases WHERE project = ?`, project).Scan(&raw, &p.Current)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return p, err
	}
	return p, json.Unmarshal([]byte(raw), &p.Names)
}

func (s *Store) SetPhases(project string, names []string) (Phases, error) {
	if names == nil {
		names = []string{}
	}
	raw, err := json.Marshal(names)
	if err != nil {
		return Phases{}, err
	}
	old, err := s.Phases(project)
	if err != nil {
		return Phases{}, err
	}
	idx := 0
	if old.Current < len(old.Names) {
		idx = max(0, slices.Index(names, old.Names[old.Current]))
	}
	_, err = s.db.Exec(`INSERT INTO ym_phases (project, phases, idx) VALUES (?, ?, ?)
		ON CONFLICT(project) DO UPDATE SET phases = excluded.phases, idx = excluded.idx`, project, string(raw), idx)
	if err != nil {
		return Phases{}, err
	}
	return s.Phases(project)
}

func (s *Store) NextPhase(project string) (Phases, error) {
	p, err := s.Phases(project)
	if err != nil {
		return p, err
	}
	if p.Current+1 >= len(p.Names) {
		return p, errors.New("already at the last phase")
	}
	if _, err := s.db.Exec(`UPDATE ym_phases SET idx = idx + 1 WHERE project = ? AND idx = ?`, project, p.Current); err != nil {
		return p, err
	}
	return s.Phases(project)
}

// Lineage holds the stored project structure: spawn links, names and merges.
type Lineage struct {
	Parent map[string]string
	Name   map[string]string
	Merged map[string]string
}

func (s *Store) Lineage() (Lineage, error) {
	l := Lineage{Parent: map[string]string{}, Name: map[string]string{}, Merged: map[string]string{}}
	for q, m := range map[string]map[string]string{
		`SELECT child, parent FROM ym_links`:          l.Parent,
		`SELECT project, name FROM ym_names`:          l.Name,
		`SELECT project, into_project FROM ym_merges`: l.Merged,
	} {
		rows, err := s.db.Query(q)
		if err != nil {
			return l, err
		}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				rows.Close()
				return l, err
			}
			m[k] = v
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return l, err
		}
	}
	return l, nil
}

func (s *Store) Link(child, parent string) error {
	if child == parent {
		return errors.New("a session cannot spawn itself")
	}
	_, err := s.db.Exec(`INSERT INTO ym_links (child, parent) VALUES (?, ?) ON CONFLICT(child) DO UPDATE SET parent = excluded.parent`, child, parent)
	return err
}

func (s *Store) Rename(project, name string) error {
	if strings.TrimSpace(name) == "" {
		_, err := s.db.Exec(`DELETE FROM ym_names WHERE project = ?`, project)
		return err
	}
	_, err := s.db.Exec(`INSERT INTO ym_names (project, name) VALUES (?, ?) ON CONFLICT(project) DO UPDATE SET name = excluded.name`, project, name)
	return err
}

func (s *Store) Merge(project, into string) error {
	if project == into {
		return errors.New("cannot merge a project into itself")
	}
	_, err := s.db.Exec(`INSERT INTO ym_merges (project, into_project) VALUES (?, ?) ON CONFLICT(project) DO UPDATE SET into_project = excluded.into_project`, project, into)
	return err
}

// Split undoes every merge into or out of project and reports how many it undid.
func (s *Store) Split(project string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM ym_merges WHERE project = ? OR into_project = ?`, project, project)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *Store) LoadScan(path string) (int64, []byte, bool) {
	var off int64
	var data []byte
	if s.db.QueryRow(`SELECT offset, data FROM ym_scans WHERE path = ?`, path).Scan(&off, &data) != nil {
		return 0, nil, false
	}
	return off, data, true
}

func (s *Store) SaveScan(path string, offset int64, data []byte) error {
	_, err := s.db.Exec(`INSERT INTO ym_scans (path, offset, data) VALUES (?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET offset = excluded.offset, data = excluded.data`, path, offset, data)
	return err
}

// PRStatuses returns the last fetched status of every pull request ym knows.
func (s *Store) PRStatuses() (map[string]PRStatus, error) {
	rows, err := s.db.Query(`SELECT url, state, title, review, passed, failed, pending, head, commits, threads, unresolved, fetched_at, merge_details FROM ym_prs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PRStatus{}
	for rows.Next() {
		var url string
		var st PRStatus
		var details []byte
		var at sql.NullString
		if err := rows.Scan(&url, &st.State, &st.Title, &st.Review, &st.Passed, &st.Failed, &st.Pending, &st.Head, &st.Commits, &st.Threads, &st.Unresolved, &at, &details); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(details, &st.Merge); err != nil {
			return nil, err
		}
		st.Fetched = parseTime(at)
		out[url] = st
	}
	return out, rows.Err()
}

func (s *Store) SavePRStatus(url string, st PRStatus) error {
	details, err := json.Marshal(st.Merge)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ym_prs (url, state, title, review, passed, failed, pending, head, commits, threads, unresolved, fetched_at, merge_details)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET state = excluded.state, title = excluded.title, review = excluded.review,
		passed = excluded.passed, failed = excluded.failed, pending = excluded.pending, head = excluded.head, commits = excluded.commits,
		threads = excluded.threads, unresolved = excluded.unresolved, fetched_at = excluded.fetched_at, merge_details = excluded.merge_details`,
		url, st.State, st.Title, st.Review, st.Passed, st.Failed, st.Pending, st.Head, st.Commits, st.Threads, st.Unresolved,
		st.Fetched.UTC().Format(time.RFC3339Nano), details)
	return err
}

// prThreads adds the head, commit and review thread columns to an older ym_prs and marks every row due again.
func prThreads(db *sql.DB) error {
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_prs') WHERE name = 'unresolved'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_prs') WHERE name = 'unresolved'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	for _, q := range []string{
		`ALTER TABLE ym_prs ADD COLUMN head TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE ym_prs ADD COLUMN commits INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE ym_prs ADD COLUMN threads INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE ym_prs ADD COLUMN unresolved INTEGER NOT NULL DEFAULT 0`,
		`DELETE FROM ym_prs`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}
