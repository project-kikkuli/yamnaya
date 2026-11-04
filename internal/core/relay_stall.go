package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"
)

// RelayStallAfter is how long a host may leave a relay untaken before ym calls it stalled. Delivery normally takes under 3 minutes,
// and every wait past 10 minutes in ym_relay's history lasted 32 minutes or more.
const RelayStallAfter = 15 * time.Minute

// relayStall is one outage of a host that stopped taking relays: since when, the owner's item about it, and the inboxes told.
type relayStall struct {
	Dest, Item string
	Since      time.Time
	Told       []string
}

func (s *Store) relayStalls() (map[string]relayStall, error) {
	rows, err := s.db.Query(`SELECT dest, since, item, told FROM ym_relay_stalls`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]relayStall{}
	for rows.Next() {
		var st relayStall
		var since int64
		var told string
		if err := rows.Scan(&st.Dest, &since, &st.Item, &told); err != nil {
			return nil, err
		}
		st.Since = time.Unix(0, since)
		if err := json.Unmarshal([]byte(told), &st.Told); err != nil {
			return nil, err
		}
		out[st.Dest] = st
	}
	return out, rows.Err()
}

func (s *Store) saveRelayStall(st relayStall) error {
	told, err := json.Marshal(st.Told)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO ym_relay_stalls (dest, since, item, told) VALUES (?, ?, ?, ?)
		ON CONFLICT(dest) DO UPDATE SET item = excluded.item, told = excluded.told`, st.Dest, st.Since.UnixNano(), st.Item, string(told))
	return err
}

// watchRelays opens an outage for each host that left a relay untaken past RelayStallAfter, wakes whoever queued a command for it,
// tells the owner once, and says so again when nothing stale waits there any more. A relay the host refused shows it does take them.
func (c *Core) watchRelays(b Board) error {
	pending, err := c.Store.PendingRelays()
	if err != nil {
		return err
	}
	stalls, err := c.Store.relayStalls()
	if err != nil {
		return err
	}
	now := c.now()
	waiting := map[string][]Relay{}
	oldest := map[string]time.Time{}
	for _, r := range pending {
		if r.Refusals > 0 || r.Error != "" {
			continue
		}
		waiting[r.Dest] = append(waiting[r.Dest], r)
		if at, seen := oldest[r.Dest]; !seen || r.Created.Before(at) {
			oldest[r.Dest] = r.Created
		}
	}
	stale := func(dest string) bool {
		at, seen := oldest[dest]
		return seen && now.Sub(at) >= RelayStallAfter
	}
	var errs []error
	for dest, st := range stalls {
		if !stale(dest) {
			errs = append(errs, c.endRelayStall(st))
			delete(stalls, dest)
		}
	}
	for dest, rs := range waiting {
		st, open := stalls[dest]
		if !open {
			if !stale(dest) {
				continue
			}
			if st, err = c.openRelayStall(b, dest, oldest[dest], len(rs)); err != nil {
				errs = append(errs, err)
				continue
			}
		}
		errs = append(errs, c.tellRelayStall(b, st, rs))
	}
	return errors.Join(errs...)
}

// openRelayStall records the outage and tells the owner: an item on this host's queue, and a notification unless the notification
// would wait on the same stalled host.
func (c *Core) openRelayStall(b Board, dest string, since time.Time, n int) (relayStall, error) {
	st := relayStall{Dest: dest, Since: since}
	it := Item{Kind: KindChore, DoLine: "Get " + clip(dest, MaxDoLine-len("Get  taking relays")) + " taking relays", Recipient: Owner, Session: "ym", CreatedAt: c.now(),
		Question: fmt.Sprintf("%s has taken nothing since %s: %s wait for it, the oldest %s old.", dest, st.Since.Local().Format(time.Kitchen), plural(n, "relay"), Span(c.now().Sub(st.Since))),
		Rec:      "Wake that host or restart its ym serve. Everything queued goes through once it pulls, and this row leaves by itself."}
	it.Scope = c.ScopeOf(b, it.Session)
	added, err := c.Store.addItem(it)
	if err != nil {
		return st, err
	}
	st.Item = added.Key()
	if err := c.Store.saveRelayStall(st); err != nil {
		return st, err
	}
	self, err := c.Store.RelaySelf()
	if err != nil || self.Courier == dest {
		return st, err
	}
	return st, c.notify("ym: relay to "+dest+" stalled", it.Question)
}

// tellRelayStall wakes, once per outage, the goal or chief of everyone with a command waiting for the host, including one queued
// after the outage began.
func (c *Core) tellRelayStall(b Board, st relayStall, rs []Relay) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, r := range rs {
		if inbox := c.relayInbox(b, o, r); inbox != "" {
			counts[inbox]++
		}
	}
	var errs []error
	for inbox, n := range counts {
		if slices.Contains(st.Told, inbox) {
			continue
		}
		text := fmt.Sprintf("relay to %s stalled: it has taken nothing since %s, and %s from here wait for it. They stay queued and go when it pulls again, so do not resend them or wait on its answer.",
			st.Dest, st.Since.Local().Format(time.Kitchen), plural(n, "command"))
		if _, _, err := c.RecordGoalEvent(inbox, EventRelayStalled, text, relayStallKey("stalled", st, inbox)); err != nil {
			errs = append(errs, err)
			continue
		}
		st.Told = append(st.Told, inbox)
	}
	return errors.Join(append(errs, c.Store.saveRelayStall(st))...)
}

// relayInbox is the goal, or the chief, whose manager should hear that r waits: r's requester's for a ym command, its filer's for an
// ask, "" for anything else or when no running manager is over it.
func (c *Core) relayInbox(b Board, o Org, r Relay) string {
	if r.Origin != "" {
		return ""
	}
	session := ""
	switch r.Kind {
	case RelayYM:
		var y relayYM
		if json.Unmarshal([]byte(r.Body), &y) != nil {
			return ""
		}
		session = y.Requester
	case RelayAsk:
		var a relayAsk
		if json.Unmarshal([]byte(r.Body), &a) != nil || a.Item != c.SelfName() {
			return ""
		}
		it, err := c.Store.Item("#" + strconv.FormatInt(a.Seq, 10))
		if err != nil {
			return ""
		}
		session = it.Session
	}
	if session == "" {
		return ""
	}
	key := c.Resolve(session)
	m, ok := o.ManagerOf(key)
	if !ok {
		if m, ok = o.Managed(key, b.parents); !ok {
			return ""
		}
	}
	if !o.Active(m) {
		return ""
	}
	if _, hasGoal := o.Goals[m.GoalID]; hasGoal {
		return m.GoalID
	}
	return m.ID
}

// endRelayStall says the outage is over to everyone told of it and to the owner, whose item it closes.
func (c *Core) endRelayStall(st relayStall) error {
	took := Span(c.now().Sub(st.Since))
	var errs []error
	for _, inbox := range st.Told {
		text := fmt.Sprintf("relay to %s is back: after %s nothing queued for it is stale any more, so answers to your commands can arrive.", st.Dest, took)
		_, _, err := c.RecordGoalEvent(inbox, EventRelayBack, text, relayStallKey("back", st, inbox))
		errs = append(errs, err)
	}
	if st.Item != "" {
		if _, err := c.Store.Transition(st.Item, ItemDismissed, "the relay to "+st.Dest+" is back"); err != nil && !errors.Is(err, ErrFinal) {
			errs = append(errs, err)
		}
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if _, err := c.Store.db.Exec(`DELETE FROM ym_relay_stalls WHERE dest = ?`, st.Dest); err != nil {
		return err
	}
	return c.notify("ym: relay to "+st.Dest+" back", fmt.Sprintf("%s stalled for %s; nothing queued for it is stale any more.", st.Dest, took))
}

func relayStallKey(what string, st relayStall, inbox string) string {
	return fmt.Sprintf("relay-%s:%s:%d:%s", what, st.Dest, st.Since.UnixNano(), inbox)
}
