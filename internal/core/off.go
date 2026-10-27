package core

import (
	"errors"
	"fmt"
	"strings"
)

const offSchema = `
CREATE TABLE IF NOT EXISTS ym_off (
	target TEXT PRIMARY KEY,
	at TEXT NOT NULL
) WITHOUT ROWID;
`

// ErrOff is what every ym command that acts or delivers says when it runs inside a session ym is off for.
var ErrOff = errors.New("ym is off for this session; reply in chat instead")

// StandOff is a goal or chief the owner turned ym off for: ym leaves it alone until it is turned back on.
const StandOff = "ym off"

func (s *Store) offTargets() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT target FROM ym_off`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out[t] = true
	}
	return out, rows.Err()
}

// SetOff records ym off for target, a session id or a manager or chief id, or clears it.
func (s *Store) SetOff(target string, off bool) error {
	if off {
		_, err := s.db.Exec(`INSERT OR IGNORE INTO ym_off (target, at) VALUES (?, ?)`, target, now())
		return err
	}
	_, err := s.db.Exec(`DELETE FROM ym_off WHERE target = ?`, target)
	return err
}

// offIn reports whether ym is off for session: for it or a session it was spawned under, or for the manager or chief whose tree it
// belongs to. The walk stops at the first manager, so a chief's flag never covers the goals reporting to it.
func offIn(targets map[string]bool, o Org, parents map[string]string, session string) bool {
	if len(targets) == 0 {
		return false
	}
	seen := map[string]bool{}
	for at := session; at != "" && !seen[at]; at = parents[at] {
		seen[at] = true
		if targets[at] {
			return true
		}
		if m, ok := o.ManagerOf(at); ok {
			return targets[m.ID]
		}
	}
	return false
}

// Off reports whether ym is off for session, reading the flags afresh.
func (c *Core) Off(session string) bool {
	targets, err := c.Store.offTargets()
	if err != nil || len(targets) == 0 {
		return false
	}
	o, err := c.LoadOrg()
	if err != nil {
		return false
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return false
	}
	return offIn(targets, o, lin.Parent, c.Resolve(session))
}

// CallerOff reports whether ym is off for the session the process pid runs under.
func (c *Core) CallerOff(pid int, getenv func(string) string) bool {
	if targets, err := c.Store.offTargets(); err != nil || len(targets) == 0 {
		return false
	}
	cl, err := c.Calling(pid, getenv)
	return err == nil && c.Off(cl.Session)
}

// reach is the live session ym may deliver into: none while ym is off for it, so whatever is meant for it waits as it does for a
// session that is down. Every wake, nudge, digest, answer, message, steer and prompt ym hands a session passes here.
func (c *Core) reach(b Board, session string) (Live, bool) {
	s, ok := b.Session(session)
	if !ok {
		return s, false
	}
	targets, err := c.Store.offTargets()
	if err == nil && offIn(targets, b.Org, b.parents, s.Key()) {
		return Live{}, false
	}
	return s, true
}

// managerOff reports whether ym is off for manager or chief m.
func managerOff(targets map[string]bool, o Org, parents map[string]string, m Manager) bool {
	if m.Session == "" {
		return targets[m.ID]
	}
	return offIn(targets, o, parents, m.Session)
}

// markOff flags every session and node on the board ym is off for.
func (c *Core) markOff(b *Board) error {
	targets, err := c.Store.offTargets()
	if err != nil || len(targets) == 0 {
		return err
	}
	for i := range b.Sessions {
		b.Sessions[i].Off = offIn(targets, b.Org, b.parents, b.Sessions[i].Key())
	}
	for i := range b.Projects {
		for j := range b.Projects[i].Sessions {
			b.Projects[i].Sessions[j].Off = offIn(targets, b.Org, b.parents, b.Projects[i].Sessions[j].Key())
		}
	}
	return nil
}

// OffTarget is what ym off or on acts on: a goal's manager or a chief, or one session.
type OffTarget struct {
	Manager *Manager
	Session string
}

func (t OffTarget) key() string {
	if t.Manager != nil {
		return t.Manager.ID
	}
	return t.Session
}

func (t OffTarget) String() string {
	switch {
	case t.Manager == nil:
		return "session " + Short(t.Session)
	case t.Manager.GoalID != "":
		return "goal " + t.Manager.GoalID + " (" + t.Manager.ID + ") and every agent under it"
	}
	return "chief " + t.Manager.Name + " (" + t.Manager.ID + ") and its own agents"
}

// OffTargetOf reads ref as a goal, manager or chief, else as a session.
func (c *Core) OffTargetOf(ref string) (OffTarget, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return OffTarget{}, err
	}
	if m, err := o.lookup(ref); err == nil {
		return OffTarget{Manager: &m}, nil
	} else if !errors.Is(err, ErrNotFound) {
		return OffTarget{}, err
	}
	cl, err := c.CallerOf(ref)
	if err != nil {
		return OffTarget{}, err
	}
	return OffTarget{Session: cl.Session}, nil
}

// SetOff turns ym off, or back on, for t. Turning it on hands each session it reaches again everything held for it while it was off,
// as one message.
func (c *Core) SetOff(b Board, t OffTarget, off bool) (string, error) {
	before := c.offSessions(b)
	if err := c.Store.SetOff(t.key(), off); err != nil {
		return "", err
	}
	if off {
		return "ym is off for " + t.String() + "; ym delivers nothing to it and its ym commands refuse until ym on", nil
	}
	if t.Manager == nil && c.Off(t.Session) {
		return "", fmt.Errorf("%s is still off: ym is off for %s; ym on %s turns it on", t, c.offBy(t.Session), c.offBy(t.Session))
	}
	n := 0
	var errs []error
	for _, key := range before {
		if c.Off(key) {
			continue
		}
		sent, err := c.flush(b, key)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", Short(key), err))
		}
		if sent {
			n++
		}
	}
	msg := "ym is on for " + t.String()
	if n > 0 {
		msg += fmt.Sprintf("; handed %s what waited for them", plural(n, "session"))
	}
	return msg, errors.Join(errs...)
}

// offBy names the session, or the manager or chief, whose flag keeps ym off for session.
func (c *Core) offBy(session string) string {
	targets, _ := c.Store.offTargets()
	o, _ := c.LoadOrg()
	lin, _ := c.Store.Lineage()
	seen := map[string]bool{}
	for at := c.Resolve(session); at != "" && !seen[at]; at = lin.Parent[at] {
		seen[at] = true
		if targets[at] {
			return Short(at)
		}
		if m, ok := o.ManagerOf(at); ok {
			if m.GoalID != "" {
				return m.GoalID
			}
			return m.ID
		}
	}
	return Short(session)
}

func (c *Core) offSessions(b Board) []string {
	targets, err := c.Store.offTargets()
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range b.Sessions {
		if s.Remote == "" && offIn(targets, b.Org, b.parents, s.Key()) {
			out = append(out, s.Key())
		}
	}
	return out
}

// flush hands session, in one message, everything held for it while ym was off: its goal's or subtree's events when it is a manager
// or chief, the answers to its items and the messages queued for it.
func (c *Core) flush(b Board, session string) (bool, error) {
	o, err := c.LoadOrg()
	if err != nil {
		return false, err
	}
	var parts []string
	var done []func() error
	if m, ok := o.ManagerOf(session); ok && o.Active(m) {
		if g, ok := o.Goals[m.GoalID]; ok {
			events, err := c.Store.undelivered(g.ID)
			if err != nil {
				return false, err
			}
			if len(events) > 0 {
				parts = append(parts, wakeText(g, events))
				done = append(done, func() error { return c.Store.markDelivered(events, c.now()) })
			}
		} else if m.Role == RoleChief {
			events, err := c.chiefEvents(o, m)
			if err != nil {
				return false, err
			}
			if len(events) > 0 {
				parts = append(parts, chiefText(o, events, ""))
				done = append(done, func() error { return c.Store.markHeard(m.ID, events, c.now()) })
			}
		}
	}
	items, err := c.Inbox(session)
	if err != nil {
		return false, err
	}
	for _, it := range items {
		parts = append(parts, notice(it))
	}
	msgs, err := c.TakeMessages(session)
	if err != nil {
		return false, err
	}
	for _, m := range msgs {
		parts = append(parts, m.Text)
	}
	if len(parts) == 0 {
		return false, nil
	}
	text := "ym is back on for you. While it was off:\n\n" + strings.Join(parts, "\n\n")
	if err := c.tell(b, session, text, false); err != nil {
		return false, err
	}
	var errs []error
	for _, f := range done {
		errs = append(errs, f())
	}
	return true, errors.Join(errs...)
}
