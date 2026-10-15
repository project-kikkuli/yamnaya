package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Key names the item uniquely across scopes.
func (it Item) Key() string { return "#" + strconv.FormatInt(it.Seq, 10) }

// Qualified is the ID with its scope's name, for lists that mix scopes.
func (it Item) Qualified() string {
	if n := ScopeName(it.Scope); n != "" {
		return n + ":" + it.ID
	}
	return it.ID
}

// ScopeName is the short name of a scope: the repository's name, or the directory's.
func ScopeName(scope string) string {
	if scope == "" {
		return ""
	}
	if strings.HasSuffix(scope, ".git") {
		return GitInfo{Common: scope}.Name()
	}
	return filepath.Base(scope)
}

// scopeItems moves a database from one global item sequence to per-scope IDs: old rows keep their ID as an alias
// and wait, unnumbered, for Renumber to place them in their scopes. Events then point at items by key.
func scopeItems(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var has int
	if err := tx.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_items') WHERE name = 'scope'`).Scan(&has); err != nil {
		return err
	}
	if has == 0 {
		for _, q := range []string{
			`CREATE TABLE ym_items_scoped (
				seq INTEGER PRIMARY KEY AUTOINCREMENT, scope TEXT NOT NULL DEFAULT '', num INTEGER, id TEXT NOT NULL, alias TEXT NOT NULL DEFAULT '',
				kind TEXT NOT NULL, question TEXT NOT NULL, rec TEXT NOT NULL DEFAULT '', options TEXT NOT NULL DEFAULT '[]',
				assumption TEXT NOT NULL DEFAULT '', blocks INTEGER NOT NULL DEFAULT 0, session TEXT NOT NULL, harness TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL, state TEXT NOT NULL, answer TEXT NOT NULL DEFAULT '', reason TEXT NOT NULL DEFAULT '',
				answered_at TEXT, delivered_at TEXT)`,
			`INSERT INTO ym_items_scoped (seq, scope, num, id, alias, kind, question, rec, options, assumption, blocks, session, harness,
				created_at, state, answer, reason, answered_at, delivered_at)
				SELECT seq, '', NULL, id, id, kind, question, rec, options, assumption, blocks, session, harness,
				created_at, state, answer, reason, answered_at, delivered_at FROM ym_items`,
			`UPDATE ym_item_events SET item = '#' || (SELECT seq FROM ym_items i WHERE i.id = ym_item_events.item)
				WHERE item NOT LIKE '#%' AND EXISTS (SELECT 1 FROM ym_items i WHERE i.id = ym_item_events.item)`,
			`DROP TABLE ym_items`,
			`ALTER TABLE ym_items_scoped RENAME TO ym_items`,
		} {
			if _, err := tx.Exec(q); err != nil {
				return err
			}
		}
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS ym_items_session ON ym_items(session, state)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ym_items_scope_id ON ym_items(scope, id) WHERE num IS NOT NULL`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Unscoped lists the sessions that filed items still waiting for per-scope numbers.
func (s *Store) Unscoped() ([]string, error) {
	rows, err := s.db.Query(`SELECT DISTINCT session FROM ym_items WHERE num IS NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var sess string
		if err := rows.Scan(&sess); err != nil {
			return nil, err
		}
		out = append(out, sess)
	}
	return out, rows.Err()
}

// Renumber gives every unnumbered item the next ID of its session's scope, oldest first.
func (s *Store) Renumber(scopeOf map[string]string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT seq, id, kind, session FROM ym_items WHERE num IS NULL ORDER BY seq`)
	if err != nil {
		return err
	}
	type legacy struct {
		seq               int64
		id, kind, session string
	}
	var all []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.seq, &l.id, &l.kind, &l.session); err != nil {
			rows.Close()
			return err
		}
		all = append(all, l)
	}
	rows.Close()
	for _, l := range all {
		scope := scopeOf[l.session]
		num, err := nextNum(tx, scope)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE ym_items SET scope = ?, num = ?, id = ?, alias = CASE alias WHEN '' THEN ? ELSE alias END WHERE seq = ?`,
			scope, num, idFor(l.kind, num), l.id, l.seq); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE ym_item_events SET item = ? WHERE item = ?`, "#"+strconv.FormatInt(l.seq, 10), l.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func nextNum(tx *sql.Tx, scope string) (int64, error) {
	var n int64
	err := tx.QueryRow(`SELECT COALESCE(max(num), 0) + 1 FROM ym_items WHERE scope = ? AND num IS NOT NULL`, scope).Scan(&n)
	return n, err
}

func idFor(kind string, num int64) string { return kindPrefix[kind] + strconv.FormatInt(num, 10) }

const itemCols = `seq, scope, id, alias, kind, question, rec, options, assumption, blocks, session, harness, created_at, state, answer, reason, answered_at, delivered_at`

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var opts string
	var created, answered, delivered sql.NullString
	if err := row.Scan(&it.Seq, &it.Scope, &it.ID, &it.Alias, &it.Kind, &it.Question, &it.Rec, &opts, &it.Assumption, &it.Blocks, &it.Session, &it.Harness,
		&created, &it.State, &it.Answer, &it.Reason, &answered, &delivered); err != nil {
		return it, err
	}
	if err := json.Unmarshal([]byte(opts), &it.Options); err != nil {
		return it, err
	}
	it.CreatedAt, it.AnsweredAt, it.Delivered = parseTime(created), parseTime(answered), parseTime(delivered)
	return it, nil
}

// AddItem files it in its Scope with that scope's next ID.
func (s *Store) AddItem(it Item) (Item, error) {
	if _, ok := kindPrefix[it.Kind]; !ok {
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
	num, err := nextNum(tx, it.Scope)
	if err != nil {
		return it, err
	}
	it.ID, it.State, it.CreatedAt = idFor(it.Kind, num), ItemOpen, time.Now().UTC()
	err = tx.QueryRow(`INSERT INTO ym_items (scope, num, id, kind, question, rec, options, assumption, blocks, session, harness, created_at, state)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING seq`,
		it.Scope, num, it.ID, it.Kind, it.Question, it.Rec, string(opts), it.Assumption, it.Blocks, it.Session, it.Harness,
		it.CreatedAt.Format(time.RFC3339Nano), it.State).Scan(&it.Seq)
	if err != nil {
		return it, err
	}
	if err := addEvent(tx, it.Key(), "", ItemOpen, ""); err != nil {
		return it, err
	}
	it.History = []Event{{At: it.CreatedAt, To: ItemOpen}}
	return it, tx.Commit()
}

func addEvent(tx *sql.Tx, key, from, to, text string) error {
	_, err := tx.Exec(`INSERT INTO ym_item_events (item, at, from_state, to_state, text) VALUES (?, ?, ?, ?, ?)`, key, now(), from, to, text)
	return err
}

// Item resolves ref from anywhere: a key (#12), name:ID, an ID only one scope has, or an old global ID.
func (s *Store) Item(ref string) (Item, error) { return s.Find("", ref) }

// Find resolves ref as seen from scope: a key (#12), name:ID, an ID in scope, an old global ID, then an ID only one scope has.
func (s *Store) Find(scope, ref string) (Item, error) {
	ref = strings.TrimSpace(ref)
	one := func(q string, args ...any) ([]Item, error) {
		rows, err := s.db.Query(`SELECT `+itemCols+` FROM ym_items WHERE `+q+` ORDER BY seq`, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []Item
		for rows.Next() {
			it, err := scanItem(rows)
			if err != nil {
				return nil, err
			}
			out = append(out, it)
		}
		return out, rows.Err()
	}
	var tries []func() ([]Item, error)
	if seq, ok := strings.CutPrefix(ref, "#"); ok {
		tries = append(tries, func() ([]Item, error) { return one(`seq = ?`, seq) })
	} else if name, id, ok := strings.Cut(ref, ":"); ok {
		tries = append(tries, func() ([]Item, error) {
			all, err := one(`id = ?`, strings.ToUpper(id))
			var out []Item
			for _, it := range all {
				if strings.EqualFold(ScopeName(it.Scope), name) || it.Scope == name {
					out = append(out, it)
				}
			}
			return out, err
		})
	} else {
		id := strings.ToUpper(ref)
		tries = append(tries,
			func() ([]Item, error) { return one(`scope = ? AND id = ? AND num IS NOT NULL`, scope, id) },
			func() ([]Item, error) { return one(`alias = ?`, id) },
			func() ([]Item, error) { return one(`id = ?`, id) },
		)
	}
	for _, try := range tries {
		found, err := try()
		if err != nil {
			return Item{}, err
		}
		switch len(found) {
		case 0:
			continue
		case 1:
			return s.history(found[0])
		}
		var names []string
		for _, it := range found {
			names = append(names, it.Qualified())
		}
		return Item{}, fmt.Errorf("item %s is ambiguous: %s; pass -p <project> or name:ID", ref, strings.Join(names, ", "))
	}
	return Item{}, fmt.Errorf("item %s: %w", ref, ErrNotFound)
}

func (s *Store) history(it Item) (Item, error) {
	rows, err := s.db.Query(`SELECT at, from_state, to_state, text FROM ym_item_events WHERE item = ? ORDER BY seq`, it.Key())
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
	Scopes   []string
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
	in("scope", f.Scopes)
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

// Transition moves the item ref names to state to; text is the answer or the dismissal reason.
func (s *Store) Transition(ref, to, text string) (Item, error) {
	it, err := s.Item(ref)
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
	res, err := tx.Exec(`UPDATE ym_items SET state = ?, answer = ?, reason = ?, answered_at = ?, delivered_at = NULL WHERE seq = ? AND state = ?`,
		to, answer, reason, answeredAt, it.Seq, it.State)
	if err != nil {
		return it, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return it, fmt.Errorf("item %s changed concurrently; retry", it.ID)
	}
	if err := addEvent(tx, it.Key(), it.State, to, text); err != nil {
		return it, err
	}
	if err := tx.Commit(); err != nil {
		return it, err
	}
	return s.Item(it.Key())
}

func (s *Store) MarkDelivered(ref string) error {
	it, err := s.Item(ref)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE ym_items SET delivered_at = ? WHERE seq = ?`, now(), it.Seq)
	return err
}

// Spoken is how an answer names the item to its agent: the ID the agent was given when it filed it.
func (it Item) Spoken() string {
	if it.Alias != "" {
		return it.Alias
	}
	return it.ID
}

// Locator finds where a session worked, even one that no longer runs.
type Locator interface {
	SessionCwd(session string) string
}

// ScopeOf is the scope items filed by session go in: its repository, else the repository its mission's crew writes in, else its directory.
func (c *Core) ScopeOf(b Board, session string) string {
	if s, ok := b.Session(session); ok {
		if s.RepoKey != "" {
			return s.RepoKey
		}
		if p, ok := b.Project(b.ProjectOf(session)); ok && p.Repo != "" {
			return p.Repo
		}
		if s.Cwd != "" {
			return s.Cwd
		}
	}
	for _, h := range c.Harnesses {
		l, ok := h.(Locator)
		if !ok {
			continue
		}
		if cwd := l.SessionCwd(session); cwd != "" {
			if g := c.gitFor(cwd, c.now()); g.Common != "" {
				return g.Common
			}
			return cwd
		}
	}
	return ""
}

// ScopeItems numbers items filed before IDs were per scope, placing each in its session's scope. Their old IDs stay as aliases.
func (c *Core) ScopeItems() error {
	sessions, err := c.Store.Unscoped()
	if err != nil || len(sessions) == 0 {
		return err
	}
	b, err := c.Board()
	if err != nil {
		return err
	}
	scopes := map[string]string{}
	for _, s := range sessions {
		scopes[s] = c.ScopeOf(b, s)
	}
	return c.Store.Renumber(scopes)
}
