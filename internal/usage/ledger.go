package usage

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
	_ "modernc.org/sqlite"
)

const ledgerSchema = `
CREATE TABLE IF NOT EXISTS ym_usage_files (
	id INTEGER PRIMARY KEY,
	path TEXT NOT NULL UNIQUE,
	harness TEXT NOT NULL,
	session TEXT NOT NULL,
	root INTEGER NOT NULL,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	offset INTEGER NOT NULL,
	cursor TEXT NOT NULL DEFAULT '',
	cost REAL NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS ym_usage_responses (
	id TEXT PRIMARY KEY,
	file INTEGER NOT NULL,
	model TEXT NOT NULL,
	day TEXT NOT NULL,
	tier INTEGER NOT NULL,
	fast INTEGER NOT NULL,
	us INTEGER NOT NULL,
	input INTEGER NOT NULL,
	output INTEGER NOT NULL,
	cache_read INTEGER NOT NULL,
	cache_5m INTEGER NOT NULL,
	cache_1h INTEGER NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS ym_usage_sums (
	file INTEGER NOT NULL,
	model TEXT NOT NULL,
	day TEXT NOT NULL,
	tier INTEGER NOT NULL,
	fast INTEGER NOT NULL,
	us INTEGER NOT NULL,
	input INTEGER NOT NULL,
	output INTEGER NOT NULL,
	cache_read INTEGER NOT NULL,
	cache_5m INTEGER NOT NULL,
	cache_1h INTEGER NOT NULL,
	PRIMARY KEY (file, model, day, tier, fast, us)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS ym_usage_limits (
	name TEXT PRIMARY KEY,
	harness TEXT NOT NULL,
	body TEXT NOT NULL
);
`

// file is a source plus how far it has been read and what reading it must remember.
type file struct {
	source
	ID     int64
	Offset int64
	Cursor codexCursor
	Cost   float64
}

type sumKey struct {
	File       int64
	Model, Day string
	Class      class
}

// stored is a response as the ledger keeps it, under the file that first showed it.
type stored struct {
	sumKey
	Tokens core.Tokens
}

type ledger struct {
	db *sql.DB
}

func openLedger(path string) (*ledger, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "temp_store(MEMORY)")
	q.Add("_pragma", "cache_size(-65536)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+path+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ledgerSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init usage schema: %w", err)
	}
	return &ledger{db: db}, nil
}

func (l *ledger) close() error { return l.db.Close() }

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func (l *ledger) load() (map[string]*file, map[sumKey]core.Tokens, map[string]core.Limit, error) {
	files := map[string]*file{}
	rows, err := l.db.Query(`SELECT id, path, harness, session, root, size, mtime, offset, cursor, cost FROM ym_usage_files`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		f := &file{}
		var cursor string
		if err := rows.Scan(&f.ID, &f.Path, &f.Harness, &f.Session, &f.Root, &f.Size, &f.MTime, &f.Offset, &cursor, &f.Cost); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		if cursor != "" {
			json.Unmarshal([]byte(cursor), &f.Cursor)
		}
		files[f.Path] = f
	}
	rows.Close()
	sums := map[sumKey]core.Tokens{}
	rows, err = l.db.Query(`SELECT file, model, day, tier, fast, us, input, output, cache_read, cache_5m, cache_1h FROM ym_usage_sums`)
	if err != nil {
		return nil, nil, nil, err
	}
	for rows.Next() {
		var k sumKey
		var t core.Tokens
		if err := rows.Scan(&k.File, &k.Model, &k.Day, &k.Class.Tier, &k.Class.Fast, &k.Class.US, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m, &t.CacheWrite1h); err != nil {
			rows.Close()
			return nil, nil, nil, err
		}
		sums[k] = t
	}
	rows.Close()
	limits := map[string]core.Limit{}
	rows, err = l.db.Query(`SELECT name, body FROM ym_usage_limits`)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var name, body string
		var lim core.Limit
		if rows.Scan(&name, &body) == nil && json.Unmarshal([]byte(body), &lim) == nil {
			limits[name] = lim
		}
	}
	return files, sums, limits, rows.Err()
}

// responses reads the stored responses among ids.
func (l *ledger) responses(ids []string) (map[string]*stored, error) {
	out := map[string]*stored{}
	for start := 0; start < len(ids); start += 500 {
		chunk := ids[start:min(start+500, len(ids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := l.db.Query(`SELECT id, file, model, day, tier, fast, us, input, output, cache_read, cache_5m, cache_1h FROM ym_usage_responses WHERE id IN (?`+strings.Repeat(`,?`, len(chunk)-1)+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			s := &stored{}
			t := &s.Tokens
			if err := rows.Scan(&id, &s.File, &s.Model, &s.Day, &s.Class.Tier, &s.Class.Fast, &s.Class.US, &t.Input, &t.Output, &t.CacheRead, &t.CacheWrite5m, &t.CacheWrite1h); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = s
		}
		rows.Close()
	}
	return out, nil
}

// batch is everything one scan writes.
type batch struct {
	reset     []int64
	files     []*file
	responses map[string]*stored
	sums      map[sumKey]core.Tokens
	limits    []core.Limit
}

func (l *ledger) commit(b batch) error {
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, id := range b.reset {
		for _, q := range []string{`DELETE FROM ym_usage_sums WHERE file = ?`, `DELETE FROM ym_usage_responses WHERE file = ?`} {
			if _, err := tx.Exec(q, id); err != nil {
				return err
			}
		}
	}
	if err := insertResponses(tx, b.responses); err != nil {
		return err
	}
	st, err := tx.Prepare(`INSERT INTO ym_usage_sums (file, model, day, tier, fast, us, input, output, cache_read, cache_5m, cache_1h)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(file, model, day, tier, fast, us) DO UPDATE SET input = input + excluded.input, output = output + excluded.output,
		cache_read = cache_read + excluded.cache_read, cache_5m = cache_5m + excluded.cache_5m, cache_1h = cache_1h + excluded.cache_1h`)
	if err != nil {
		return err
	}
	for k, t := range b.sums {
		if _, err := st.Exec(k.File, k.Model, k.Day, k.Class.Tier, b2i(k.Class.Fast), b2i(k.Class.US), t.Input, t.Output, t.CacheRead, t.CacheWrite5m, t.CacheWrite1h); err != nil {
			return err
		}
	}
	st.Close()
	st, err = tx.Prepare(`INSERT INTO ym_usage_files (id, path, harness, session, root, size, mtime, offset, cursor, cost) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET size = excluded.size, mtime = excluded.mtime, offset = excluded.offset, cursor = excluded.cursor, cost = excluded.cost`)
	if err != nil {
		return err
	}
	for _, f := range b.files {
		cursor := ""
		if f.Harness == codexName {
			c, _ := json.Marshal(f.Cursor)
			cursor = string(c)
		}
		if _, err := st.Exec(f.ID, f.Path, f.Harness, f.Session, b2i(f.Root), f.Size, f.MTime, f.Offset, cursor, f.Cost); err != nil {
			return err
		}
	}
	st.Close()
	for _, lim := range b.limits {
		body, _ := json.Marshal(lim)
		if _, err := tx.Exec(`INSERT INTO ym_usage_limits (name, harness, body) VALUES (?, ?, ?) ON CONFLICT(name) DO UPDATE SET body = excluded.body`,
			lim.Harness+"/"+lim.Name, lim.Harness, string(body)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// insertResponses writes responses a hundred rows per statement, which a cold scan needs to stay fast.
func insertResponses(tx *sql.Tx, responses map[string]*stored) error {
	const per = 100
	const cols = 12
	ids := make([]string, 0, len(responses))
	for id := range responses {
		ids = append(ids, id)
	}
	for start := 0; start < len(ids); start += per {
		chunk := ids[start:min(start+per, len(ids))]
		args := make([]any, 0, len(chunk)*cols)
		for _, id := range chunk {
			s := responses[id]
			t := s.Tokens
			args = append(args, id, s.File, s.Model, s.Day, s.Class.Tier, b2i(s.Class.Fast), b2i(s.Class.US), t.Input, t.Output, t.CacheRead, t.CacheWrite5m, t.CacheWrite1h)
		}
		row := "(?" + strings.Repeat(", ?", cols-1) + ")"
		q := `INSERT INTO ym_usage_responses (id, file, model, day, tier, fast, us, input, output, cache_read, cache_5m, cache_1h) VALUES ` +
			row + strings.Repeat(", "+row, len(chunk)-1) +
			` ON CONFLICT(id) DO UPDATE SET input = excluded.input, output = excluded.output, cache_read = excluded.cache_read, cache_5m = excluded.cache_5m, cache_1h = excluded.cache_1h`
		if _, err := tx.Exec(q, args...); err != nil {
			return err
		}
	}
	return nil
}
