package core

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// Goal event kinds, recorded by ym serve without a model, by a manager's ym goal update, and by verification.
const (
	EventFinished     = "agent-finished"
	EventAsked        = "asked"
	EventPROpened     = "pr-opened"
	EventCIRed        = "ci-red"
	EventCIGreen      = "ci-green"
	EventReview       = "review"
	EventMerged       = "merged"
	EventClosed       = "closed"
	EventConflict     = "conflict"
	EventStuck        = "stuck"
	EventDone         = "done"
	EventVerifying    = "verifying"
	EventVerified     = "verified"
	EventFailedVerify = "failed-verify"
	EventUpdate       = "update"
	EventTimer        = "timer"
	EventNudge        = "nudge"
	EventAgentStopped = "agent-stopped"
	EventAgentHung    = "agent-hung"
	EventAgentAdrift  = "agent-adrift"
)

// ownerKinds are the events that count as unread news in the owner's feed; the rest show in it but never ask for attention.
var ownerKinds = map[string]bool{
	EventPROpened: true, EventCIRed: true, EventMerged: true, EventClosed: true, EventStuck: true, EventDone: true,
	EventFailedVerify: true, EventUpdate: true,
}

// chiefKinds are the state changes a chief hears from every goal below it.
var chiefKinds = map[string]bool{
	EventPROpened: true, EventCIRed: true, EventCIGreen: true, EventReview: true, EventMerged: true, EventClosed: true, EventConflict: true,
	EventStuck: true, EventDone: true, EventVerifying: true, EventVerified: true, EventFailedVerify: true, EventUpdate: true,
	EventAgentStopped: true, EventAgentHung: true, EventAgentAdrift: true,
	EventSteered: true, EventAdopted: true,
}

// notifyKinds are the only events that also raise a desktop notification.
var notifyKinds = map[string]bool{EventDone: true, EventStuck: true}

// GoalEvent is one thing that happened to a goal: its manager is woken with it, its chiefs hear it, the owner reads it in the feed.
type GoalEvent struct {
	Seq  int64
	Goal string
	Kind string
	// Payload is the event in words, as the manager, chiefs and owner read it.
	Payload string
	// Key makes the event happen once: a second event with the same non-empty key is not recorded.
	Key       string
	Created   time.Time
	Delivered time.Time
	Seen      time.Time
}

// OwnerNews reports whether the event counts as unread news for the owner.
func (e GoalEvent) OwnerNews() bool { return ownerKinds[e.Kind] }

const eventSchema = `
CREATE TABLE IF NOT EXISTS ym_goal_events (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	goal TEXT NOT NULL,
	kind TEXT NOT NULL,
	payload TEXT NOT NULL DEFAULT '',
	key TEXT NOT NULL DEFAULT '',
	created TEXT NOT NULL,
	delivered_to_manager TEXT,
	seen_by_owner TEXT,
	notified TEXT
);
CREATE UNIQUE INDEX IF NOT EXISTS ym_goal_events_key ON ym_goal_events(key) WHERE key != '';
CREATE INDEX IF NOT EXISTS ym_goal_events_goal ON ym_goal_events(goal, seq);
CREATE TABLE IF NOT EXISTS ym_goal_event_deliveries (
	event INTEGER NOT NULL,
	manager TEXT NOT NULL,
	at TEXT NOT NULL,
	PRIMARY KEY (event, manager)
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS ym_goal_timers (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	manager TEXT NOT NULL,
	goal TEXT NOT NULL DEFAULT '',
	due TEXT NOT NULL,
	why TEXT NOT NULL,
	created TEXT NOT NULL,
	fired TEXT
);
`

const eventCols = `seq, goal, kind, payload, key, created, delivered_to_manager, seen_by_owner`

func scanEvents(rows *sql.Rows, err error) ([]GoalEvent, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GoalEvent
	for rows.Next() {
		var e GoalEvent
		var created, delivered, seen sql.NullString
		if err := rows.Scan(&e.Seq, &e.Goal, &e.Kind, &e.Payload, &e.Key, &created, &delivered, &seen); err != nil {
			return nil, err
		}
		e.Created, e.Delivered, e.Seen = parseTime(created), parseTime(delivered), parseTime(seen)
		out = append(out, e)
	}
	return out, rows.Err()
}

// AddGoalEvent records e, created now unless it says when; added is false when an event with its key exists already. An event
// that is Delivered already, such as the manager's own update, never wakes the manager.
func (s *Store) AddGoalEvent(e GoalEvent) (GoalEvent, bool, error) {
	if e.Created.IsZero() {
		e.Created = time.Now().UTC()
	}
	e.Payload = strings.TrimSpace(e.Payload)
	err := s.db.QueryRow(`INSERT INTO ym_goal_events (goal, kind, payload, key, created, delivered_to_manager) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING RETURNING seq`, e.Goal, e.Kind, e.Payload, e.Key, stamp(e.Created), stamp(e.Delivered)).Scan(&e.Seq)
	if errors.Is(err, sql.ErrNoRows) {
		return e, false, nil
	}
	return e, err == nil, err
}

// GoalPRs maps each goal to the pull requests opened for it, in the order they opened.
func (s *Store) GoalPRs() (map[string][]string, error) {
	rows, err := s.db.Query(`SELECT goal, key FROM ym_goal_events WHERE kind = ? ORDER BY seq`, EventPROpened)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][]string{}
	for rows.Next() {
		var goal, key string
		if err := rows.Scan(&goal, &key); err != nil {
			return nil, err
		}
		if _, url, ok := strings.Cut(key, ":pr:"); ok {
			out[goal] = append(out[goal], url)
		}
	}
	return out, rows.Err()
}

// GoalEvents lists the goal's events, oldest first.
func (s *Store) GoalEvents(goal string) ([]GoalEvent, error) {
	return scanEvents(s.db.Query(`SELECT `+eventCols+` FROM ym_goal_events WHERE goal = ? ORDER BY seq`, goal))
}

// undelivered lists the goal's events its manager has not been woken with, oldest first.
func (s *Store) undelivered(goal string) ([]GoalEvent, error) {
	return scanEvents(s.db.Query(`SELECT `+eventCols+` FROM ym_goal_events WHERE goal = ? AND delivered_to_manager IS NULL ORDER BY seq`, goal))
}

func (s *Store) markDelivered(events []GoalEvent, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if _, err := tx.Exec(`UPDATE ym_goal_events SET delivered_to_manager = ? WHERE seq = ? AND delivered_to_manager IS NULL`, stamp(at), e.Seq); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// lastKind is the kind of the goal's newest event, "" when it has none.
func (s *Store) lastKind(goal string) (string, error) {
	var kind string
	err := s.db.QueryRow(`SELECT kind FROM ym_goal_events WHERE goal = ? ORDER BY seq DESC LIMIT 1`, goal).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// lastDelivered is when the goal's manager was last woken with an event.
func (s *Store) lastDelivered(goal string) (time.Time, error) {
	return s.latest(`SELECT delivered_to_manager FROM ym_goal_events WHERE goal = ? AND delivered_to_manager IS NOT NULL`, goal, true)
}

// latest is the latest, or with !last the earliest, of the times q selects.
func (s *Store) latest(q, arg string, last bool) (time.Time, error) {
	rows, err := s.db.Query(q, arg)
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	var out time.Time
	for rows.Next() {
		var at sql.NullString
		if err := rows.Scan(&at); err != nil {
			return out, err
		}
		if t := parseTime(at); out.IsZero() || (last && t.After(out)) || (!last && t.Before(out)) {
			out = t
		}
	}
	return out, rows.Err()
}

// chiefPending lists the events of goals that chief has not heard, created since since, oldest first.
func (s *Store) chiefPending(chief string, goals []string, since time.Time) ([]GoalEvent, error) {
	if len(goals) == 0 {
		return nil, nil
	}
	args := []any{chief}
	for _, g := range goals {
		args = append(args, g)
	}
	events, err := scanEvents(s.db.Query(`SELECT `+eventCols+` FROM ym_goal_events e WHERE NOT EXISTS
		(SELECT 1 FROM ym_goal_event_deliveries d WHERE d.event = e.seq AND d.manager = ?) AND goal IN (?`+
		strings.Repeat(", ?", len(goals)-1)+`) ORDER BY seq`, args...))
	if err != nil {
		return nil, err
	}
	var out []GoalEvent
	for _, e := range events {
		if chiefKinds[e.Kind] && !e.Created.Before(since) {
			out = append(out, e)
		}
	}
	return out, nil
}

func (s *Store) markHeard(chief string, events []GoalEvent, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, e := range events {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO ym_goal_event_deliveries (event, manager, at) VALUES (?, ?, ?)`, e.Seq, chief, stamp(at)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// firstRun is when manager first ran as any session, zero when it never has.
func (s *Store) firstRun(manager string) (time.Time, error) {
	return s.latest(`SELECT at FROM ym_manager_sessions WHERE manager = ?`, manager, false)
}

// MarkSeen marks every event of the goal read by the owner.
func (s *Store) MarkSeen(goal string, at time.Time) error {
	_, err := s.db.Exec(`UPDATE ym_goal_events SET seen_by_owner = ? WHERE goal = ? AND seen_by_owner IS NULL`, stamp(at), goal)
	return err
}

// Feed is a goal's news for the owner: how much is unread, and its newest events.
type Feed struct {
	Unread int
	// Latest is the newest event that counts as news, if any.
	Latest *GoalEvent
	// Recent are the goal's newest events, newest first.
	Recent []GoalEvent
}

// feedRecent is how many of a goal's newest events the board carries.
const feedRecent = 30

// Feeds is every goal's feed.
func (s *Store) Feeds() (map[string]Feed, error) {
	events, err := scanEvents(s.db.Query(`SELECT `+eventCols+` FROM (SELECT *, row_number() OVER (PARTITION BY goal ORDER BY seq DESC) AS n
		FROM ym_goal_events) WHERE n <= ? OR (seen_by_owner IS NULL AND kind IN ('`+strings.Join(keys(ownerKinds), "', '")+`')) ORDER BY seq DESC`, feedRecent))
	if err != nil {
		return nil, err
	}
	out := map[string]Feed{}
	for _, e := range events {
		f := out[e.Goal]
		if e.OwnerNews() {
			if e.Seen.IsZero() {
				f.Unread++
			}
			if f.Latest == nil {
				f.Latest = &e
			}
		}
		if len(f.Recent) < feedRecent {
			f.Recent = append(f.Recent, e)
		}
		out[e.Goal] = f
	}
	return out, nil
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// unnotified lists the events that raise a desktop notification and have not, oldest first.
func (s *Store) unnotified() ([]GoalEvent, error) {
	return scanEvents(s.db.Query(`SELECT ` + eventCols + ` FROM ym_goal_events WHERE notified IS NULL AND kind IN ('` +
		strings.Join(keys(notifyKinds), "', '") + `') ORDER BY seq`))
}

func (s *Store) markNotified(seq int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE ym_goal_events SET notified = ? WHERE seq = ?`, stamp(at), seq)
	return err
}

// Timer is a wake a manager or chief set for itself.
type Timer struct {
	Seq     int64
	Manager string
	Goal    string
	Due     time.Time
	Why     string
	Created time.Time
	Fired   time.Time
}

func (s *Store) AddTimer(t Timer) (Timer, error) {
	err := s.db.QueryRow(`INSERT INTO ym_goal_timers (manager, goal, due, why, created) VALUES (?, ?, ?, ?, ?) RETURNING seq`,
		t.Manager, t.Goal, stamp(t.Due), t.Why, stamp(t.Created)).Scan(&t.Seq)
	return t, err
}

// Timers lists the timers that have not fired, soonest first.
func (s *Store) Timers() ([]Timer, error) {
	rows, err := s.db.Query(`SELECT seq, manager, goal, due, why, created FROM ym_goal_timers WHERE fired IS NULL ORDER BY due, seq`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Timer
	for rows.Next() {
		var t Timer
		var due, created sql.NullString
		if err := rows.Scan(&t.Seq, &t.Manager, &t.Goal, &due, &t.Why, &created); err != nil {
			return nil, err
		}
		t.Due, t.Created = parseTime(due), parseTime(created)
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) fireTimer(seq int64, at time.Time) error {
	_, err := s.db.Exec(`UPDATE ym_goal_timers SET fired = ? WHERE seq = ?`, stamp(at), seq)
	return err
}
