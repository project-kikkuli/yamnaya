package core

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// AdoptAfter is how long a manager has to adopt a steer before it is nudged, and between nudges.
	AdoptAfter = 10 * time.Minute

	// EventSteered is a new revision of a goal's plan; its manager is woken with it.
	EventSteered = "steered"
	// EventAdopted is the manager taking a revision on; its brief must follow.
	EventAdopted = "adopted"
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
	// Pending names the chief on another host asked to confirm that Author is above it; the steer lands once it does.
	Pending  string
	Manager  string
	Revision int
	Text     string
	// Author is Owner, or the id of the chief that steered; AuthorName is the name a chief on another host came with.
	Author     string
	AuthorName string
	// Sent says the steer left for another host, whose ym takes it or refuses it; it has no revision here.
	Sent      bool
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
func (s *Store) addSteer(manager, text, author, authorName string, at time.Time) (Steer, error) {
	defer s.invalidate()
	st := Steer{Manager: manager, Text: text, Author: author, AuthorName: authorName, CreatedAt: at}
	tx, err := s.db.Begin()
	if err != nil {
		return st, err
	}
	defer tx.Rollback()
	err = tx.QueryRow(`INSERT INTO ym_steers (manager, revision, text, author, author_name, created_at)
		VALUES (?, (SELECT coalesce(max(revision), 0) + 1 FROM ym_steers WHERE manager = ?), ?, ?, ?, ?) RETURNING revision`,
		manager, manager, text, author, authorName, stamp(at)).Scan(&st.Revision)
	if err != nil {
		return st, err
	}
	var goal string
	if err = tx.QueryRow(`SELECT goal_id FROM ym_managers WHERE 'mgr'||num=?`, manager).Scan(&goal); err != nil {
		return st, err
	}
	if goal != "" {
		if _, err = workIn(tx, goal, fmt.Sprintf("steer:%s:%d", manager, st.Revision), "scope steered", at); err != nil {
			return st, err
		}
	}
	return st, tx.Commit()
}

func (s *Store) saveSteer(st Steer) error {
	_, err := s.db.Exec(`UPDATE ym_steers SET adopted_at=?, adopted_session=?, nudges=?, nudged_at=?, raised_at=? WHERE manager=? AND revision=?`,
		stamp(st.AdoptedAt), st.AdoptedSession, st.Nudges, stamp(st.NudgedAt), stamp(st.RaisedAt), st.Manager, st.Revision)
	return err
}

const steerCols = `manager, revision, text, author, author_name, created_at, adopted_at, adopted_session, nudges, nudged_at, raised_at`

func scanSteer(row interface{ Scan(...any) error }) (Steer, error) {
	var st Steer
	var created, adopted, nudged, raised sql.NullString
	err := row.Scan(&st.Manager, &st.Revision, &st.Text, &st.Author, &st.AuthorName, &created, &adopted, &st.AdoptedSession, &st.Nudges, &nudged, &raised)
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
	return remembered(s, "steers", cloneMap[string, Steer], s.latestSteers)
}

func (s *Store) latestSteers() (map[string]Steer, error) {
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

// PlanContext is what a goal or chief is about now, as ym goal context --json prints it for another host.
type PlanContext struct {
	Manager  Manager `json:"manager"`
	Text     string  `json:"text"`
	Revision int     `json:"revision"`
}

// Context is what the goal or chief ref names is about now: its newest revision's text, else the goal's ask.
func (c *Core) Context(ref string) (Manager, string, int, error) {
	if host, rest, ok := c.remoteHost(ref); ok {
		out, err := c.RunRemote(host, "goal", "context", "--json", rest)
		if err != nil {
			return Manager{}, "", 0, err
		}
		var ctx PlanContext
		if err := json.Unmarshal([]byte(out), &ctx); err != nil {
			return Manager{}, "", 0, fmt.Errorf("ym goal context on %s: %w", host, err)
		}
		ctx.Manager.ID, ctx.Manager.Host = RemoteKey(host, ctx.Manager.ID), host
		return ctx.Manager, ctx.Text, ctx.Revision, nil
	}
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
	return m, o.Ask(m), 0, nil
}

// Ask is what m started out to do: its goal's ask, or the remit a chief was started with.
func (o Org) Ask(m Manager) string {
	if m.Role == RoleChief {
		return m.Remit
	}
	return o.Goals[m.GoalID].Ask
}

// SteerContext records text as the next revision of what the goal or chief ref names is about, and tells its manager to adopt it.
// author is Owner, or the manager or chief steering: a chief steers only what reports to it, at any depth.
func (c *Core) SteerContext(b Board, ref, text, author string) (Steer, error) {
	return c.SteerContextAs(b, ref, text, author, "")
}

// SteerContextAs is SteerContext for a chief on another host that came with its name.
func (c *Core) SteerContextAs(b Board, ref, text, author, authorName string) (Steer, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Steer{}, errors.New("a steer needs the new plan in words")
	}
	if host, rest, ok := c.remoteHost(ref); ok {
		args := []string{"goal", "steer", "--text", text}
		if author != Owner {
			args = append(args, "--by", RemoteKey(c.SelfName(), author))
		}
		y := relayYM{Args: append(args, rest)}
		if author != Owner {
			y.ByName = c.RefName(author)
		}
		if _, err := c.runOrRelay(host, y); err != nil {
			return Steer{}, fmt.Errorf("%s: %w", c.RefName(ref), err)
		}
		return Steer{Manager: ref, Text: text, Author: author, Sent: true}, nil
	}
	o, err := c.LoadOrg()
	if err != nil {
		return Steer{}, err
	}
	m, err := o.lookup(ref)
	if err != nil {
		return Steer{}, err
	}
	if g := o.Goals[m.GoalID]; m.Retired || g.State == GoalKilled || g.State == GoalDropped {
		return Steer{}, Final(fmt.Errorf("%s has ended; only a running goal or chief takes a steer", m.Shown()))
	}
	if author != Owner {
		reports, via := climb(o, author, m.ID)
		if !reports && via != "" {
			host, rest, _ := SplitRemote(via)
			v := relayVouch{Chief: rest, Author: c.fromHere(author), AuthorName: cmp.Or(authorName, c.RefName(author)), Target: RemoteKey(c.SelfName(), m.ID), Text: text}
			return Steer{Manager: m.ID, Text: text, Author: author, Pending: via}, c.Relay(host, RelayVouch, v)
		}
		if !reports {
			return Steer{}, Final(fmt.Errorf("%s does not report to %s; a chief steers only its own reports", m.Shown(), c.RefName(author)))
		}
	}
	return c.applySteer(b, o, m, text, author, authorName)
}

// applySteer records text as m's next revision by author and tells m to adopt it.
func (c *Core) applySteer(b Board, o Org, m Manager, text, author, authorName string) (Steer, error) {
	if latest, err := c.Store.LatestSteers(); err != nil {
		return Steer{}, err
	} else if st, ok := latest[m.ID]; ok && st.Text == text {
		return st, Final(fmt.Errorf("%s is already at revision %d with that plan", m.Shown(), st.Revision))
	}
	if _, _, remote := SplitRemote(author); remote {
		authorName = c.actorName(author, authorName)
	}
	st, err := c.Store.addSteer(m.ID, text, author, authorName, c.now())
	if err != nil {
		return st, err
	}
	text = steerText(o, m, st)
	if g, ok := o.Goals[m.GoalID]; ok {
		_, _, err := c.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventSteered, Payload: text, Key: fmt.Sprintf("%s:steer:%d", g.ID, st.Revision), Created: st.CreatedAt})
		return st, err
	}
	if m.Session != "" {
		if err := c.tell(b, m.Session, "ym: "+text, author == Owner); err != nil {
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

// fromHere names author so another host cannot read it as its own manager of that id: a manager or chief here gets this host's name.
func (c *Core) fromHere(author string) string {
	if _, _, remote := SplitRemote(author); remote {
		return author
	}
	return RemoteKey(c.SelfName(), author)
}

// hereFrom undoes fromHere on the host author runs on.
func (c *Core) hereFrom(author string) string {
	if h, rest, remote := SplitRemote(author); remote && h == c.SelfName() {
		return rest
	}
	return author
}

// climb walks up from id's chief toward chief, past any retired chief, whose reports report one level up. It reports whether it
// reached chief, which must not be retired itself; when the walk ends at a chief on another host first, via names that chief, whose
// host knows the rest of the way.
func climb(o Org, chief, id string) (reports bool, via string) {
	seen := map[string]bool{id: true}
	for at := o.Managers[id].ParentID; at != "" && !seen[at]; at = o.Managers[at].ParentID {
		if at == chief {
			m, ok := o.Managers[at]
			return !ok || !m.Retired, ""
		}
		seen[at] = true
		_, ok := o.Managers[at]
		if _, _, remote := SplitRemote(at); !ok && remote {
			return false, at
		}
		if !ok {
			return false, ""
		}
	}
	return false, ""
}

func subject(o Org, m Manager) string {
	if g, ok := o.Goals[m.GoalID]; ok {
		return "goal " + g.Shown()
	}
	return "chief " + m.Shown()
}

func steerText(o Org, m Manager, st Steer) string {
	reopened := ""
	if g, ok := o.Goals[m.GoalID]; ok && (g.State == GoalVerifying || g.State == GoalDone) {
		reopened = " Your ym goal done was for the plan before it, so the goal is active again and its verification is called off."
	}
	return fmt.Sprintf("%s steered %s.%s Revision %d of what it is about replaces earlier direction where they differ:\n\n%s\n\nRework your plan to fit it: change course for your agents and pull requests as it needs, then run ym goal adopt %s --revision %d and record the change with ym goal brief.",
		o.SteerBy(st), subject(o, m), reopened, st.Revision, st.Text, m.Shown(), st.Revision)
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
			return Steer{}, fmt.Errorf("%s runs %s, not %s; only the one steered adopts its steer", Short(session), me.Shown(), m.Shown())
		}
	}
	all, err := c.Store.Steers(m.ID)
	if err != nil {
		return Steer{}, err
	}
	if len(all) == 0 {
		return Steer{}, fmt.Errorf("%s has never been steered: %w", m.Shown(), ErrNotFound)
	}
	st := all[len(all)-1]
	if revision != st.Revision {
		return st, fmt.Errorf("revision %d is not %s's newest; adopt revision %d, which replaces it", revision, m.Shown(), st.Revision)
	}
	if st.Adopted() {
		return st, nil
	}
	st.AdoptedAt, st.AdoptedSession = c.now(), session
	if err := c.Store.saveSteer(st); err != nil {
		return st, err
	}
	if g, ok := o.Goals[m.GoalID]; ok {
		_, _, err = c.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventAdopted, Payload: fmt.Sprintf("%s adopted revision %d of the plan", m.Shown(), st.Revision),
			Key: fmt.Sprintf("%s:adopt:%d", g.ID, st.Revision), Created: st.AdoptedAt, Delivered: st.AdoptedAt})
	}
	return st, err
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
				st.Revision, subject(o, m), o.SteerBy(st), now.Sub(st.CreatedAt).Round(time.Minute), m.Shown(), m.Shown(), st.Revision)
			if err := c.tell(b, m.Session, text, false); err != nil {
				errs = append(errs, err)
				continue
			}
			st.Nudges, st.NudgedAt = st.Nudges+1, now
			errs = append(errs, c.Store.saveSteer(st))
			continue
		}
		if up := raisedTo(o, st); up != Owner {
			shown, about := m.Shown(), subject(o, m)
			if _, _, remote := SplitRemote(up); remote {
				shown = RemoteKey(c.SelfName(), m.Shown())
				about = "chief " + shown
				if g, ok := o.Goals[m.GoalID]; ok {
					about = "goal " + RemoteKey(c.SelfName(), g.Shown())
				}
			}
			text := fmt.Sprintf("ym: %s has not adopted revision %d of %s, which %s sent, after %d nudges. Open it, relaunch it with ym goal relaunch %s, or steer again.",
				m.Role+" "+shown, st.Revision, about, o.SteerBy(st), st.Nudges, shown)
			if err := c.tellManager(b, o, up, text); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		st.RaisedAt = now
		errs = append(errs, c.Store.saveSteer(st))
	}
	return errors.Join(errs...)
}
