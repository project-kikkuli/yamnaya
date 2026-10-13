package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	db *sql.DB
}

const schema = `
CREATE TABLE IF NOT EXISTS projects (
	name TEXT PRIMARY KEY,
	cwd TEXT NOT NULL,
	state TEXT NOT NULL,
	phases TEXT NOT NULL DEFAULT '[]',
	phase_index INTEGER NOT NULL DEFAULT 0,
	item_seq INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	landed_at TEXT
);
CREATE TABLE IF NOT EXISTS windows (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	project TEXT NOT NULL REFERENCES projects(name),
	name TEXT NOT NULL,
	role TEXT NOT NULL,
	harness TEXT NOT NULL,
	session_id TEXT NOT NULL DEFAULT '',
	prompt TEXT NOT NULL DEFAULT '',
	tmux_target TEXT NOT NULL DEFAULT '',
	managed INTEGER NOT NULL DEFAULT 1,
	created_at TEXT NOT NULL,
	closed_at TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS windows_open_name ON windows(project, name) WHERE closed_at IS NULL;
CREATE TABLE IF NOT EXISTS items (
	project TEXT NOT NULL REFERENCES projects(name),
	seq INTEGER NOT NULL,
	id TEXT NOT NULL,
	kind TEXT NOT NULL,
	question TEXT NOT NULL,
	rec TEXT NOT NULL DEFAULT '',
	options TEXT NOT NULL DEFAULT '[]',
	assumption TEXT NOT NULL DEFAULT '',
	source_window TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	state TEXT NOT NULL,
	answer TEXT NOT NULL DEFAULT '',
	reason TEXT NOT NULL DEFAULT '',
	answered_at TEXT,
	PRIMARY KEY (project, id),
	UNIQUE (project, seq)
);
CREATE TABLE IF NOT EXISTS item_events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	project TEXT NOT NULL,
	item TEXT NOT NULL,
	at TEXT NOT NULL,
	from_state TEXT NOT NULL,
	to_state TEXT NOT NULL,
	text TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS item_events_item ON item_events(project, item);
DROP TABLE IF EXISTS stage;
CREATE TABLE IF NOT EXISTS staged (
	slot INTEGER PRIMARY KEY CHECK (slot = 1),
	window_id INTEGER REFERENCES windows(id),
	origin TEXT NOT NULL,
	origin_session TEXT NOT NULL,
	pane TEXT NOT NULL
);
`

func DefaultPath() (string, error) {
	if dir := os.Getenv("YAMNAYA_STATE_DIR"); dir != "" {
		return filepath.Join(dir, "ym.db"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "yamnaya", "ym.db"), nil
}

func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	if err := addColumns(db, "staged", map[string]string{
		"harness":    "TEXT NOT NULL DEFAULT ''",
		"session_id": "TEXT NOT NULL DEFAULT ''",
		"cwd":        "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	if err := addColumns(db, "projects", map[string]string{
		"git_dir": "TEXT NOT NULL DEFAULT ''",
		"branch":  "TEXT NOT NULL DEFAULT ''",
	}); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate schema: %w", err)
	}
	return &Store{db: db}, nil
}

// addColumns adds each column table lacks.
func addColumns(db *sql.DB, table string, cols map[string]string) error {
	rows, err := db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return err
		}
		have[name] = true
	}
	rows.Close()
	names := make([]string, 0, len(cols))
	for name := range cols {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if have[name] {
			continue
		}
		if _, err := db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + name + ` ` + cols[name]); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
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
