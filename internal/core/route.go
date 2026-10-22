package core

import (
	"fmt"
	"strings"
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
		o.order = append(o.order, m.ID)
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

// Ordered lists the managers and chiefs oldest first.
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

// Above is the first working manager or chief at or above id: one that is retired, stuck, or whose goal ended is passed over,
// so work never waits on it. Owner when the climb runs out.
func (o Org) Above(id string) string {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		seen[id] = true
		m, ok := o.Managers[id]
		if !ok {
			return Owner
		}
		g, hasGoal := o.Goals[m.GoalID]
		ended := hasGoal && (g.State == GoalDone || g.State == GoalDropped)
		if !m.Retired && !m.Stuck() && !ended {
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

// Managed is the manager whose tree session belongs to, the manager itself for its own session.
func (o Org) Managed(session string, parents map[string]string) (Manager, bool) {
	seen := map[string]bool{}
	for at := session; at != "" && !seen[at]; at = parents[at] {
		seen[at] = true
		if m, ok := o.ManagerOf(at); ok {
			return m, true
		}
	}
	return Manager{}, false
}

// Subtree lists id and every manager and chief below it.
func (o Org) Subtree(id string) []Manager {
	var out []Manager
	var walk func(string, map[string]bool)
	walk = func(id string, seen map[string]bool) {
		if seen[id] {
			return
		}
		seen[id] = true
		m, ok := o.Managers[id]
		if !ok {
			return
		}
		out = append(out, m)
		for _, child := range o.order {
			if o.Managers[child].ParentID == id {
				walk(child, seen)
			}
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
	return c.file(b, err, it, "")
}

// AddHandoffItem files the item a captured handoff asks for, once per key, routed the way AddItem routes.
func (c *Core) AddHandoffItem(key string, it Item) (Item, error) {
	b, err := c.Board()
	return c.file(b, err, it, key)
}

// AddHandoffItemOn is AddHandoffItem on a board the caller already read.
func (c *Core) AddHandoffItemOn(b Board, key string, it Item) (Item, error) {
	return c.file(b, nil, it, key)
}

func (c *Core) file(b Board, berr error, it Item, key string) (Item, error) {
	if it.Scope == "" {
		it.Scope = c.ScopeOf(b, it.Session)
	}
	o, err := c.LoadOrg()
	if err != nil {
		return it, err
	}
	it.Recipient = o.Recipient(c.Resolve(it.Session), c.parentsFor(b, berr))
	added, fresh, err := c.Store.addItem(it, key)
	if err != nil || !fresh || added.Recipient == Owner {
		return added, err
	}
	m := o.Managers[added.Recipient]
	return added, c.tell(b, m.Session, askText(b, o, added), false)
}

// askText is how an ask reaches the manager or chief it was routed to.
func askText(b Board, o Org, it Item) string {
	from := "agent " + Short(it.Session)
	if s, ok := b.Session(it.Session); ok && s.Title != "" {
		from += " (" + oneLine(s.Title) + ")"
	}
	if m, ok := o.ManagerOf(it.Session); ok {
		from = m.Label()
		if g, ok := o.Goals[m.GoalID]; ok {
			from += " on goal " + g.ID + " \"" + oneLine(g.Ask) + "\""
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "ym: %s asks you (%s %s, %s): %s", from, it.Key(), it.ID, it.Kind, oneLine(it.Question))
	if it.Rec != "" {
		fmt.Fprintf(&sb, " Recommended: %s.", oneLine(it.Rec))
	}
	for _, opt := range it.Options {
		fmt.Fprintf(&sb, " Option %s: %s.", opt.Label, oneLine(opt.Effect))
	}
	fmt.Fprintf(&sb, " You own this: answer with ym item answer '%s' <answer> (ym item go '%s' to say go ahead, ym item dismiss '%s' <reason>)."+
		" Escalate with your own ym item add only if it is beyond your authority.", it.Key(), it.Key(), it.Key())
	return sb.String()
}

// tell hands text to the session: typed or sent when it runs on the board, else queued for its next inbox or relaunch.
// owner marks text as the owner's own words.
func (c *Core) tell(b Board, session, text string, owner bool) error {
	if s, ok := b.Session(session); ok {
		switch {
		case owner && c.Launches != nil:
			_, err := c.Launches.Send(s, text)
			return err
		case !owner && c.BackgroundSend != nil:
			return c.BackgroundSend(s, text)
		case c.message(s.Session, "msg: "+text, owner) == nil:
			_, err := c.Store.AddMessage(session, text, MsgSent)
			return err
		}
	}
	_, err := c.Store.AddMessage(session, text, MsgPending)
	return err
}

// carrier is the session that carries id's conversation now: its continuation, or the current session of the manager it ran as.
func (c *Core) carrier(o Org, id string) string {
	id = c.Resolve(id)
	if m, ok := o.ManagerOf(id); ok && m.Session != "" {
		return m.Session
	}
	return id
}
