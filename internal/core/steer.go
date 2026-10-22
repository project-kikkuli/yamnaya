package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// AdoptAfter is how long a manager has to adopt a steer before it is nudged, and between nudges.
	AdoptAfter = 10 * time.Minute
)

const steerSchema = `
CREATE TABLE IF NOT EXISTS ym_steers (
	manager TEXT NOT NULL,
	revision INTEGER NOT NULL,
	text TEXT NOT NULL,
	author TEXT NOT NULL DEFAULT '',
	created_at TEXT NOT NULL,
	adopted_at TEXT,
	adopted_session TEXT NOT NULL DEFAULT '',
	nudges INTEGER NOT NULL DEFAULT 0,
	nudged_at TEXT,
	raised_at TEXT,
	PRIMARY KEY (manager, revision)
);
`

// Steer is one revision of what a goal or chief is about: the plan its manager works to, replacing the revisions before it.
type Steer struct {
	Manager  string
	Revision int
	Text     string
	// Author is Owner, or the id of the chief that steered.
	Author    string
	CreatedAt time.Time
	AdoptedAt time.Time
	// AdoptedSession is the session that adopted the revision.
	AdoptedSession string
	Nudges         int
	NudgedAt       time.Time
	// RaisedAt is when ym told the author the revision was still not adopted after every nudge.
	RaisedAt time.Time
}

func (s Steer) Adopted() bool { return !s.AdoptedAt.IsZero() }

// By names who steered, for messages.
func (s Steer) By() string {
	if s.Author == Owner {
		return "the owner"
	}
	return "chief " + s.Author
}

func (s *Store) addSteer(manager, text, author string, at time.Time) (Steer, error) {
	st := Steer{Manager: manager, Text: text, Author: author, CreatedAt: at}
	err := s.db.QueryRow(`INSERT INTO ym_steers (manager, revision, text, author, created_at)
		VALUES (?, (SELECT coalesce(max(revision), 0) + 1 FROM ym_steers WHERE manager = ?), ?, ?, ?) RETURNING revision`,
		manager, manager, text, author, stamp(at)).Scan(&st.Revision)
	return st, err
}

func (s *Store) saveSteer(st Steer) error {
	_, err := s.db.Exec(`UPDATE ym_steers SET adopted_at=?, adopted_session=?, nudges=?, nudged_at=?, raised_at=? WHERE manager=? AND revision=?`,
		stamp(st.AdoptedAt), st.AdoptedSession, st.Nudges, stamp(st.NudgedAt), stamp(st.RaisedAt), st.Manager, st.Revision)
	return err
}

const steerCols = `manager, revision, text, author, created_at, adopted_at, adopted_session, nudges, nudged_at, raised_at`

func scanSteer(row interface{ Scan(...any) error }) (Steer, error) {
	var st Steer
	var created, adopted, nudged, raised sql.NullString
	err := row.Scan(&st.Manager, &st.Revision, &st.Text, &st.Author, &created, &adopted, &st.AdoptedSession, &st.Nudges, &nudged, &raised)
	st.CreatedAt, st.AdoptedAt, st.NudgedAt, st.RaisedAt = parseTime(created), parseTime(adopted), parseTime(nudged), parseTime(raised)
	return st, err
}

// Steers lists manager's revisions, oldest first.
func (s *Store) Steers(manager string) ([]Steer, error) {
	rows, err := s.db.Query(`SELECT `+steerCols+` FROM ym_steers WHERE manager = ? ORDER BY revision`, manager)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Steer
	for rows.Next() {
		st, err := scanSteer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

// LatestSteers maps each steered manager to its newest revision.
func (s *Store) LatestSteers() (map[string]Steer, error) {
	rows, err := s.db.Query(`SELECT ` + steerCols + ` FROM ym_steers s WHERE revision = (SELECT max(revision) FROM ym_steers WHERE manager = s.manager)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Steer{}
	for rows.Next() {
		st, err := scanSteer(rows)
		if err != nil {
			return nil, err
		}
		out[st.Manager] = st
	}
	return out, rows.Err()
}

// Context is what the goal or chief ref names is about now: its newest revision's text, else the goal's ask.
func (c *Core) Context(ref string) (Manager, string, int, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Manager{}, "", 0, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return m, "", 0, err
	}
	latest, err := c.Store.LatestSteers()
	if err != nil {
		return m, "", 0, err
	}
	if st, ok := latest[m.ID]; ok {
		return m, st.Text, st.Revision, nil
	}
	return m, o.Goals[m.GoalID].Ask, 0, nil
}

// SteerContext records text as the next revision of what the goal or chief ref names is about, and tells its manager to adopt it.
// author is Owner, or the manager or chief steering: a chief steers only what reports to it, at any depth.
func (c *Core) SteerContext(b Board, ref, text, author string) (Steer, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Steer{}, errors.New("a steer needs the new plan in words")
	}
	o, err := c.LoadOrg()
	if err != nil {
		return Steer{}, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return Steer{}, err
	}
	if !o.Active(m) {
		return Steer{}, fmt.Errorf("%s has ended; only a running goal or chief takes a steer", m.ID)
	}
	if author != Owner {
		if !below(o, author, m.ID) {
			return Steer{}, fmt.Errorf("%s does not report to %s; a chief steers only its own reports", m.ID, author)
		}
	}
	if latest, err := c.Store.LatestSteers(); err != nil {
		return Steer{}, err
	} else if st, ok := latest[m.ID]; ok && st.Text == text {
		return st, fmt.Errorf("%s is already at revision %d with that plan", m.ID, st.Revision)
	}
	st, err := c.Store.addSteer(m.ID, text, author, c.now())
	if err != nil {
		return st, err
	}
	if m.Session != "" {
		if err := c.tell(b, m.Session, steerText(o, m, st), author == Owner); err != nil {
			return st, err
		}
	}
	return st, nil
}

// raisedTo is who hears that st went unadopted: the chief that steered, or the first working one above it, else the owner.
func raisedTo(o Org, st Steer) string {
	if st.Author == Owner {
		return Owner
	}
	return o.Above(st.Author)
}

// below reports whether id reports to chief, directly or through other chiefs.
func below(o Org, chief, id string) bool {
	for _, m := range o.Subtree(chief) {
		if m.ID == id && id != chief {
			return true
		}
	}
	return false
}

func subject(o Org, m Manager) string {
	if g, ok := o.Goals[m.GoalID]; ok {
		return "goal " + g.ID
	}
	return "chief " + m.ID
}

func steerText(o Org, m Manager, st Steer) string {
	return fmt.Sprintf("ym: %s steered %s. Revision %d of what it is about replaces earlier direction where they differ:\n\n%s\n\nRework your plan to fit it: change course for your agents and pull requests as it needs, then run ym goal adopt %s --revision %d and record the change with ym goal brief.",
		st.By(), subject(o, m), st.Revision, st.Text, m.ID, st.Revision)
}

// Adopt records that the calling manager or chief took revision of its own plan on; only its newest revision can be adopted.
func (c *Core) Adopt(session, ref string, revision int) (Steer, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return Steer{}, err
	}
	session = c.Resolve(session)
	me, ok := o.ManagerOf(session)
	if !ok {
		return Steer{}, fmt.Errorf("session %s is not a manager or chief; only the one steered adopts its steer", Short(session))
	}
	m := me
	if ref != "" {
		if m, err = o.lookup(ref); err != nil {
			return Steer{}, err
		}
		if m.ID != me.ID {
			return Steer{}, fmt.Errorf("%s runs %s, not %s; only the one steered adopts its steer", Short(session), me.ID, m.ID)
		}
	}
	all, err := c.Store.Steers(m.ID)
	if err != nil {
		return Steer{}, err
	}
	if len(all) == 0 {
		return Steer{}, fmt.Errorf("%s has never been steered: %w", m.ID, ErrNotFound)
	}
	st := all[len(all)-1]
	if revision != st.Revision {
		return st, fmt.Errorf("revision %d is not %s's newest; adopt revision %d, which replaces it", revision, m.ID, st.Revision)
	}
	if st.Adopted() {
		return st, nil
	}
	st.AdoptedAt, st.AdoptedSession = c.now(), session
	return st, c.Store.saveSteer(st)
}

// watchSteers nudges a running manager that has not adopted its newest steer, and once every nudge went unanswered tells whoever
// steered: the owner through the queue, a chief in its session.
func (c *Core) watchSteers(b Board, o Org) error {
	latest, err := c.Store.LatestSteers()
	if err != nil {
		return err
	}
	now := c.now()
	var errs []error
	for _, m := range o.Ordered() {
		st, ok := latest[m.ID]
		if !ok || st.Adopted() || !o.Active(m) || !b.Alive(m) {
			continue
		}
		since := st.CreatedAt
		if st.NudgedAt.After(since) {
			since = st.NudgedAt
		}
		if now.Sub(since) < AdoptAfter || !st.RaisedAt.IsZero() {
			continue
		}
		if st.Nudges < Nudges {
			text := fmt.Sprintf("ym: you have not adopted revision %d of %s, which %s sent %s ago. Read it with ym goal context %s, rework your plan to fit, then run ym goal adopt %s --revision %d.",
				st.Revision, subject(o, m), st.By(), now.Sub(st.CreatedAt).Round(time.Minute), m.ID, m.ID, st.Revision)
			if err := c.tell(b, m.Session, text, false); err != nil {
				errs = append(errs, err)
				continue
			}
			st.Nudges, st.NudgedAt = st.Nudges+1, now
			errs = append(errs, c.Store.saveSteer(st))
			continue
		}
		if up := raisedTo(o, st); up != Owner {
			text := fmt.Sprintf("ym: %s has not adopted revision %d of %s, which %s sent, after %d nudges. Open it, relaunch it with ym goal relaunch %s, or steer again.",
				m.Label(), st.Revision, subject(o, m), st.By(), st.Nudges, m.ID)
			if err := c.tell(b, o.Managers[up].Session, text, false); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		st.RaisedAt = now
		errs = append(errs, c.Store.saveSteer(st))
	}
	return errors.Join(errs...)
}
