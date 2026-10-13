package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
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

type Item struct {
	Project      string
	ID           string
	Kind         string
	Question     string
	Rec          string
	Options      []Option
	Assumption   string
	SourceWindow string
	CreatedAt    time.Time
	State        string
	Answer       string
	Reason       string
	AnsweredAt   time.Time
	History      []Event
}

// Event is one state change of an item; reopening keeps earlier answers and dismissals here.
type Event struct {
	At   time.Time
	From string
	To   string
	Text string
}

const itemCols = `project, id, kind, question, rec, options, assumption, source_window, created_at, state, answer, reason, answered_at`

func scanItem(row interface{ Scan(...any) error }) (Item, error) {
	var it Item
	var opts string
	var created, answered sql.NullString
	if err := row.Scan(&it.Project, &it.ID, &it.Kind, &it.Question, &it.Rec, &opts, &it.Assumption, &it.SourceWindow, &created, &it.State, &it.Answer, &it.Reason, &answered); err != nil {
		return it, err
	}
	if err := json.Unmarshal([]byte(opts), &it.Options); err != nil {
		return it, err
	}
	it.CreatedAt = parseTime(created)
	it.AnsweredAt = parseTime(answered)
	return it, nil
}

// AddItem allocates the next per-project sequence number, so ids are never reused even across kinds.
func (s *Store) AddItem(it Item) (Item, error) {
	prefix, ok := kindPrefix[it.Kind]
	if !ok {
		return it, fmt.Errorf("unknown kind %q (want decision, default, or chore)", it.Kind)
	}
	if strings.TrimSpace(it.Question) == "" {
		return it, errors.New("question is required")
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
	var seq int
	err = tx.QueryRow(`UPDATE projects SET item_seq = item_seq + 1 WHERE name = ? AND state = ? RETURNING item_seq`, it.Project, ProjectActive).Scan(&seq)
	if errors.Is(err, sql.ErrNoRows) {
		return it, fmt.Errorf("no active project %q", it.Project)
	}
	if err != nil {
		return it, err
	}
	it.ID = fmt.Sprintf("%s%d", prefix, seq)
	it.State = ItemOpen
	it.CreatedAt = time.Now().UTC()
	if _, err := tx.Exec(`INSERT INTO items (project, seq, id, kind, question, rec, options, assumption, source_window, created_at, state) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		it.Project, seq, it.ID, it.Kind, it.Question, it.Rec, string(opts), it.Assumption, it.SourceWindow, it.CreatedAt.Format(time.RFC3339Nano), it.State); err != nil {
		return it, err
	}
	if err := addEvent(tx, it.Project, it.ID, "", ItemOpen, ""); err != nil {
		return it, err
	}
	it.History = []Event{{At: it.CreatedAt, To: ItemOpen}}
	return it, tx.Commit()
}

func addEvent(tx *sql.Tx, project, item, from, to, text string) error {
	_, err := tx.Exec(`INSERT INTO item_events (project, item, at, from_state, to_state, text) VALUES (?, ?, ?, ?, ?, ?)`, project, item, now(), from, to, text)
	return err
}

func (s *Store) Item(project, id string) (Item, error) {
	it, err := scanItem(s.db.QueryRow(`SELECT `+itemCols+` FROM items WHERE project = ? AND id = ?`, project, strings.ToUpper(id)))
	if errors.Is(err, sql.ErrNoRows) {
		return it, fmt.Errorf("item %s in project %q: %w", id, project, ErrNotFound)
	}
	if err != nil {
		return it, err
	}
	rows, err := s.db.Query(`SELECT at, from_state, to_state, text FROM item_events WHERE project = ? AND item = ? ORDER BY seq`, project, it.ID)
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

func (s *Store) Items(project string, all bool) ([]Item, error) {
	q := `SELECT ` + itemCols + ` FROM items WHERE project = ?`
	if !all {
		q += ` AND state = 'open'`
	}
	rows, err := s.db.Query(q+` ORDER BY seq`, project)
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

func (s *Store) OpenItemCounts(project string) (total int, byWindow map[string]int, err error) {
	rows, err := s.db.Query(`SELECT source_window, count(*) FROM items WHERE project = ? AND state = 'open' GROUP BY source_window`, project)
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()
	byWindow = map[string]int{}
	for rows.Next() {
		var w string
		var n int
		if err := rows.Scan(&w, &n); err != nil {
			return 0, nil, err
		}
		byWindow[w] = n
		total += n
	}
	return total, byWindow, rows.Err()
}

var transitions = map[string]map[string]bool{
	ItemAnswered:  {ItemOpen: true, ItemSnoozed: true},
	ItemDismissed: {ItemOpen: true, ItemSnoozed: true},
	ItemSnoozed:   {ItemOpen: true},
	ItemOpen:      {ItemAnswered: true, ItemDismissed: true, ItemSnoozed: true},
}

func (s *Store) Answer(project, id, answer string) (Item, error) {
	return s.transition(project, id, ItemAnswered, answer, "")
}

func (s *Store) Dismiss(project, id, reason string) (Item, error) {
	return s.transition(project, id, ItemDismissed, "", reason)
}

func (s *Store) Snooze(project, id string) (Item, error) {
	return s.transition(project, id, ItemSnoozed, "", "")
}

func (s *Store) Reopen(project, id string) (Item, error) {
	return s.transition(project, id, ItemOpen, "", "")
}

func (s *Store) transition(project, id, to, answer, reason string) (Item, error) {
	it, err := s.Item(project, id)
	if err != nil {
		return it, err
	}
	if !transitions[to][it.State] {
		return it, fmt.Errorf("item %s is %s; cannot move it to %s", it.ID, it.State, to)
	}
	var answeredAt any
	if to == ItemAnswered || to == ItemDismissed {
		answeredAt = now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return it, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE items SET state = ?, answer = ?, reason = ?, answered_at = ? WHERE project = ? AND id = ? AND state = ?`,
		to, answer, reason, answeredAt, project, it.ID, it.State)
	if err != nil {
		return it, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return it, fmt.Errorf("item %s changed concurrently; retry", it.ID)
	}
	if err := addEvent(tx, project, it.ID, it.State, to, answer+reason); err != nil {
		return it, err
	}
	if err := tx.Commit(); err != nil {
		return it, err
	}
	return s.Item(project, it.ID)
}
