package core

import (
	"errors"
	"fmt"
)

// stuckFailures is how many launches in a row may fail before a manager or chief counts as stuck.
const stuckFailures = 3

// Stuck reports whether ym gave up waiting for the manager to come up and told the owner.
func (m Manager) Stuck() bool { return m.Failures >= stuckFailures }

// Label names the manager for messages: its role, name and id.
func (m Manager) Label() string { return fmt.Sprintf("%s %s (%s)", m.Role, m.Name, m.ID) }

// Org is every manager and chief, every goal, and which manager each session ever ran as.
type Org struct {
	Managers map[string]Manager
	Goals    map[string]Goal
	// Sessions maps each session a manager has run as to the manager.
	Sessions map[string]string
	order    []string
}

func (c *Core) LoadOrg() (Org, error) {
	o := Org{Managers: map[string]Manager{}, Goals: map[string]Goal{}}
	ms, err := c.Store.Managers()
	if err != nil {
		return o, err
	}
	for _, m := range ms {
		o.Managers[m.ID] = m
		if !m.Retired {
			o.order = append(o.order, m.ID)
		}
	}
	gs, err := c.Store.Goals()
	if err != nil {
		return o, err
	}
	for _, g := range gs {
		o.Goals[g.ID] = g
	}
	o.Sessions, err = c.Store.ManagerSessions()
	return o, err
}

// Ordered lists the managers and chiefs that are not retired, oldest first.
func (o Org) Ordered() []Manager {
	out := make([]Manager, 0, len(o.order))
	for _, id := range o.order {
		out = append(out, o.Managers[id])
	}
	return out
}

// ManagerOf is the manager session runs as, if any.
func (o Org) ManagerOf(session string) (Manager, bool) {
	m, ok := o.Managers[o.Sessions[session]]
	return m, ok
}

// Active reports whether m still runs its goal or oversees its reports: not retired, and its goal, if any, not ended.
func (o Org) Active(m Manager) bool {
	g, hasGoal := o.Goals[m.GoalID]
	return !m.Retired && !(hasGoal && g.Ended())
}

// Above is the first working manager or chief at or above id: one that is retired, stuck, never launched, or whose goal ended
// is passed over, so work never waits on it. Owner when the climb runs out. A chief on another host ends the climb here; that
// host climbs on if it cannot answer.
func (o Org) Above(id string) string {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		m, ok := o.Managers[id]
		if _, _, remote := SplitRemote(id); remote && !ok {
			return id
		}
		if !ok {
			return Owner
		}
		if o.Active(m) && !m.Stuck() && m.Session != "" {
			return id
		}
		id = m.ParentID
	}
	return Owner
}

// Recipient is who an ask from session goes to: a manager's own ask climbs to its chief; an agent's goes to the nearest manager
// above it in the spawn tree; a session no manager started asks the owner.
func (o Org) Recipient(session string, parents map[string]string) string {
	if m, ok := o.ManagerOf(session); ok {
		return o.Above(m.ParentID)
	}
	seen := map[string]bool{session: true}
	for at := parents[session]; at != "" && !seen[at]; at = parents[at] {
		seen[at] = true
		if m, ok := o.ManagerOf(at); ok {
			return o.Above(m.ID)
		}
	}
	return Owner
}

// Managed is the active manager whose tree session belongs to, the manager itself for its own session.
func (o Org) Managed(session string, parents map[string]string) (Manager, bool) {
	seen := map[string]bool{}
	for at := session; at != "" && !seen[at]; at = parents[at] {
		seen[at] = true
		if m, ok := o.ManagerOf(at); ok {
			return m, o.Active(m)
		}
	}
	return Manager{}, false
}

// Subtree lists id and every manager and chief below it.
func (o Org) Subtree(id string) []Manager { return o.subtree(o.reports(), id) }

// reports maps each manager or chief to the ones reporting to it, in order.
func (o Org) reports() map[string][]string {
	reports := map[string][]string{}
	for _, child := range o.order {
		parent := o.Managers[child].ParentID
		reports[parent] = append(reports[parent], child)
	}
	return reports
}

func (o Org) subtree(reports map[string][]string, id string) []Manager {
	var out []Manager
	var walk func(string, map[string]bool)
	walk = func(id string, seen map[string]bool) {
		if seen[id] {
			return
		}
		seen[id] = true
		m, ok := o.Managers[id]
		if !ok || m.Retired {
			return
		}
		out = append(out, m)
		for _, child := range reports[id] {
			walk(child, seen)
		}
	}
	walk(id, map[string]bool{})
	return out
}

func (c *Core) parentsFor(b Board, berr error) map[string]string {
	if berr == nil && b.parents != nil {
		return b.parents
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return map[string]string{}
	}
	return lin.Parent
}

// AddItem files an item against a session, in the session's scope unless it names one, and routes it: an ask from a session a
// manager runs goes to that manager's session instead of the owner's queue.
func (c *Core) AddItem(it Item) (Item, error) {
	b, err := c.Board()
	return c.file(b, err, it)
}

func (c *Core) file(b Board, berr error, it Item) (Item, error) {
	if it.Scope == "" {
		it.Scope = c.ScopeOf(b, it.Session)
	}
	o, err := c.LoadOrg()
	if err != nil {
		return it, err
	}
	it.Recipient = o.Recipient(c.Resolve(it.Session), c.parentsFor(b, berr))
	if it.Recipient == Owner {
		if err := ready(it); err != nil {
			return it, err
		}
	}
	if it.Dossier, err = c.dossier(b, o, it); err != nil {
		return it, err
	}
	added, err := c.Store.addItem(it)
	if err != nil {
		return added, err
	}
	woken := false
	if m, ok := o.Managed(c.Resolve(it.Session), c.parentsFor(b, berr)); ok {
		if g, ok := o.Goals[m.GoalID]; ok {
			to := "the owner"
			if added.Recipient != Owner {
				to = added.Recipient
			}
			woken = added.Recipient == m.ID
			e := GoalEvent{Goal: g.ID, Kind: EventAsked, Created: c.now(),
				Payload: fmt.Sprintf("%s ask from %s to %s: %s", added.Key(), askerOf(o, it.Session), to, oneLine(added.Question))}
			if !woken {
				e.Delivered = e.Created
			}
			if _, _, err := c.Store.AddGoalEvent(e); err != nil {
				return added, err
			}
		}
	}
	if added.Recipient == Owner || woken {
		return added, nil
	}
	return added, c.askManager(b, o, added)
}

// askManager hands the item to the manager or chief it is routed to, on this host or another.
func (c *Core) askManager(b Board, o Org, it Item) error {
	if _, _, remote := SplitRemote(it.Recipient); remote {
		return c.relayAsk(b, o, it)
	}
	return c.tell(b, o.Managers[it.Recipient].Session, askText(b, o, it), false)
}

func askerOf(o Org, session string) string {
	if m, ok := o.ManagerOf(session); ok {
		return "manager " + m.ID
	}
	return "agent " + Short(session)
}

// askText is how an ask reaches the manager or chief it was routed to: one line naming the item, which ym item show prints whole.
func askText(b Board, o Org, it Item) string {
	from := "agent " + Short(it.Session)
	if m, ok := o.ManagerOf(it.Session); ok {
		from = m.Label()
		if g, ok := o.Goals[m.GoalID]; ok {
			from += " on goal " + g.ID
		}
	}
	return fmt.Sprintf("ym: %s ask from %s: %s", it.Key(), from, clip(oneLine(it.Question), lineMax))
}

// tell hands text to the session: typed or sent when it runs on the board, else queued for its next inbox or relaunch.
// owner marks text as the owner's own words.
func (c *Core) tell(b Board, session, text string, owner bool) error {
	if !owner {
		text = c.Store.Nicked(text)
	}
	if s, ok := c.reach(b, session); ok {
		var err error
		switch {
		case owner && c.Launches != nil:
			_, err = c.Launches.Send(s, text)
		case !owner && c.BackgroundSend != nil:
			err = c.BackgroundSend(s, text)
		case c.message(s.Session, "msg: "+text, owner) == nil:
			_, err = c.Store.AddMessage(session, text, MsgSent)
		default:
			err = errNoMessenger
		}
		if err == nil || errors.Is(err, ErrSubmissionUncertain) {
			return nil
		}
	}
	_, err := c.Store.AddMessage(session, text, MsgPending)
	return err
}

// reroute hands every open item waiting on a manager or chief that can no longer answer to the next one up, or to the owner.
func (c *Core) reroute(b Board, o Org) error {
	items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen}})
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Recipient == Owner {
			continue
		}
		up := o.Above(it.Recipient)
		if up == it.Recipient {
			continue
		}
		if err := c.Store.SetRecipient(it.Seq, up); err != nil {
			return err
		}
		was := it.Recipient
		it.Recipient = up
		if missing := Missing(it); up == Owner && len(missing) > 0 && o.Active(o.Managers[was]) {
			if err := c.Store.SetPreparer(it.Seq, was); err != nil {
				return err
			}
			if err := c.tell(b, o.Managers[was].Session, prepareText(it, missing), false); err != nil {
				return err
			}
		}
		if up != Owner {
			if err := c.askManager(b, o, it); err != nil {
				return err
			}
		}
	}
	return nil
}

// carrier is the session that carries id's conversation now: its continuation, or the current session of the manager it ran as.
func (c *Core) carrier(o Org, id string) string {
	id = c.Resolve(id)
	if m, ok := o.ManagerOf(id); ok && m.Session != "" {
		return m.Session
	}
	return id
}
