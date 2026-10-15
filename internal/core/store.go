package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	KindDecision = "decision"
	KindDefault  = "default"
	KindChore    = "chore"

	ItemOpen      = "open"
	ItemAnswered  = "answered"
	ItemDismissed = "dismissed"
	ItemSnoozed   = "snoozed"
)

var kindPrefix = map[string]string{KindDecision: "D", KindDefault: "V", KindChore: "C"}

type Option struct {
	Label  string `json:"label"`
	Effect string `json:"effect"`
}

// Item is a question an agent filed for the owner. IDs come from one sequence and are never reused.
type Item struct {
	ID         string
	Kind       string
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
	History   []Event
}

type Event struct {
	At   time.Time
	From string
	To   string
	Text string
}

// Store keeps items, phases, lineage links, project names and merges.
type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS ym_items (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	id TEXT NOT NULL UNIQUE,
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
CREATE INDEX IF NOT EXISTS ym_items_session ON ym_items(session, state);
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

func OpenStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
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
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
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

const itemCols = `id, kind, question, rec, options, assumption, blocks, session, harness, created_at, state, answer, reason, answered_at, delivered_at`

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var opts string
	var created, answered, delivered sql.NullString
	if err := row.Scan(&it.ID, &it.Kind, &it.Question, &it.Rec, &opts, &it.Assumption, &it.Blocks, &it.Session, &it.Harness, &created, &it.State, &it.Answer, &it.Reason, &answered, &delivered); err != nil {
		return it, err
	}
	if err := json.Unmarshal([]byte(opts), &it.Options); err != nil {
		return it, err
	}
	it.CreatedAt, it.AnsweredAt, it.Delivered = parseTime(created), parseTime(answered), parseTime(delivered)
	return it, nil
}

func (s *Store) AddItem(it Item) (Item, error) {
	prefix, ok := kindPrefix[it.Kind]
	if !ok {
		return it, fmt.Errorf("unknown kind %q (want decision, default, or chore)", it.Kind)
	}
	if strings.TrimSpace(it.Question) == "" {
		return it, errors.New("question is required")
	}
	if it.Session == "" {
		return it, errors.New("item needs a source session")
	}
	if it.Options == nil {
		it.Options = []Option{}
	}
	opts, err := json.Marshal(it.Options)
	if err != nil {
		return it, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return it, err
	}
	defer tx.Rollback()
	it.State = ItemOpen
	it.CreatedAt = time.Now().UTC()
	var seq int64
	err = tx.QueryRow(`INSERT INTO ym_items (id, kind, question, rec, options, assumption, blocks, session, harness, created_at, state)
		VALUES ('', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING seq`,
		it.Kind, it.Question, it.Rec, string(opts), it.Assumption, it.Blocks, it.Session, it.Harness, it.CreatedAt.Format(time.RFC3339Nano), it.State).Scan(&seq)
	if err != nil {
		return it, err
	}
	it.ID = fmt.Sprintf("%s%d", prefix, seq)
	if _, err := tx.Exec(`UPDATE ym_items SET id = ? WHERE seq = ?`, it.ID, seq); err != nil {
		return it, err
	}
	if err := addEvent(tx, it.ID, "", ItemOpen, ""); err != nil {
		return it, err
	}
	it.History = []Event{{At: it.CreatedAt, To: ItemOpen}}
	return it, tx.Commit()
}

func addEvent(tx *sql.Tx, item, from, to, text string) error {
	_, err := tx.Exec(`INSERT INTO ym_item_events (item, at, from_state, to_state, text) VALUES (?, ?, ?, ?, ?)`, item, now(), from, to, text)
	return err
}

func (s *Store) Item(id string) (Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+itemCols+` FROM ym_items WHERE id = ?`, strings.ToUpper(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return it, fmt.Errorf("item %s: %w", id, ErrNotFound)
	}
	if err != nil {
		return it, err
	}
	rows, err := s.db.Query(`SELECT at, from_state, to_state, text FROM ym_item_events WHERE item = ? ORDER BY seq`, it.ID)
	if err != nil {
		return it, err
	}
	defer rows.Close()
	it.History = []Event{}
	for rows.Next() {
		var e Event
		var at sql.NullString
		if err := rows.Scan(&at, &e.From, &e.To, &e.Text); err != nil {
			return it, err
		}
		e.At = parseTime(at)
		it.History = append(it.History, e)
	}
	return it, rows.Err()
}

// ItemFilter selects items; empty fields match everything.
type ItemFilter struct {
	States   []string
	Sessions []string
	// Undelivered keeps answered or dismissed items the agent has not received.
	Undelivered bool
}

func (s *Store) Items(f ItemFilter) ([]Item, error) {
	q := `SELECT ` + itemCols + ` FROM ym_items WHERE 1=1`
	var args []any
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		q += ` AND ` + col + ` IN (?` + strings.Repeat(`, ?`, len(vals)-1) + `)`
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("state", f.States)
	in("session", f.Sessions)
	if f.Undelivered {
		q += ` AND state IN ('answered', 'dismissed') AND delivered_at IS NULL`
	}
	rows, err := s.db.Query(q+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

var transitions = map[string]map[string]bool{
	ItemAnswered:  {ItemOpen: true, ItemSnoozed: true},
	ItemDismissed: {ItemOpen: true, ItemSnoozed: true},
	ItemSnoozed:   {ItemOpen: true},
	ItemOpen:      {ItemAnswered: true, ItemDismissed: true, ItemSnoozed: true},
}

func (s *Store) Transition(id, to, text string) (Item, error) {
	it, err := s.Item(id)
	if err != nil {
		return it, err
	}
	if !transitions[to][it.State] {
		return it, fmt.Errorf("item %s is %s; cannot move it to %s", it.ID, it.State, to)
	}
	var answer, reason string
	var answeredAt any
	switch to {
	case ItemAnswered:
		answer, answeredAt = text, now()
	case ItemDismissed:
		reason, answeredAt = text, now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return it, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE ym_items SET state = ?, answer = ?, reason = ?, answered_at = ?, delivered_at = NULL WHERE id = ? AND state = ?`,
		to, answer, reason, answeredAt, it.ID, it.State)
	if err != nil {
		return it, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return it, fmt.Errorf("item %s changed concurrently; retry", it.ID)
	}
	if err := addEvent(tx, it.ID, it.State, to, text); err != nil {
		return it, err
	}
	if err := tx.Commit(); err != nil {
		return it, err
	}
	return s.Item(it.ID)
}

func (s *Store) MarkDelivered(id string) error {
	_, err := s.db.Exec(`UPDATE ym_items SET delivered_at = ? WHERE id = ?`, now(), strings.ToUpper(id))
	return err
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
	_, err = s.db.Exec(`INSERT INTO ym_phases (project, phases, idx) VALUES (?, ?, 0)
		ON CONFLICT(project) DO UPDATE SET phases = excluded.phases, idx = max(0, min(idx, ? - 1))`, project, string(raw), len(names))
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
	rows, err := s.db.Query(`SELECT url, state, title, review, passed, failed, pending, fetched_at FROM ym_prs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]PRStatus{}
	for rows.Next() {
		var url string
		var st PRStatus
		var at sql.NullString
		if err := rows.Scan(&url, &st.State, &st.Title, &st.Review, &st.Passed, &st.Failed, &st.Pending, &at); err != nil {
			return nil, err
		}
		st.Fetched = parseTime(at)
		out[url] = st
	}
	return out, rows.Err()
}

func (s *Store) SavePRStatus(url string, st PRStatus) error {
	_, err := s.db.Exec(`INSERT INTO ym_prs (url, state, title, review, passed, failed, pending, fetched_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(url) DO UPDATE SET state = excluded.state, title = excluded.title, review = excluded.review,
		passed = excluded.passed, failed = excluded.failed, pending = excluded.pending, fetched_at = excluded.fetched_at`,
		url, st.State, st.Title, st.Review, st.Passed, st.Failed, st.Pending, st.Fetched.UTC().Format(time.RFC3339Nano))
	return err
}
