package core

import "errors"

// Resolve follows a session's continued-in records to the session that carries its conversation now.
func (c *Core) Resolve(id string) string { return c.resolve(id, "") }

func (c *Core) resolve(id, near string) string {
	seen := map[string]bool{id: true}
	for {
		next := c.successor(id, near)
		if next == "" || seen[next] {
			return id
		}
		seen[next] = true
		id = next
	}
}

func (c *Core) successor(id, near string) string {
	for _, h := range c.Harnesses {
		if ct, ok := h.(Continuer); ok {
			if next := ct.Successor(id, near); next != "" {
				return next
			}
		}
	}
	return ""
}

// carries reports, per session id, whether its conversation now runs as session; a conversation continues beside the session it left.
func (c *Core) carries(session string) func(id string) bool {
	now := c.resolve(session, session)
	known := map[string]bool{session: true, now: true}
	if o, err := c.LoadOrg(); err == nil {
		if m, ok := o.ManagerOf(now); ok {
			for past, id := range o.Sessions {
				if id == m.ID {
					known[past] = true
				}
			}
		}
	}
	return func(id string) bool {
		got, ok := known[id]
		if !ok {
			got = c.resolve(id, now) == now
			known[id] = got
		}
		return got
	}
}

// Direct hands text to a running session's own inbox, as the owner's own words when owner is set; false when its harness has none
// or the inbox did not take it.
func (c *Core) Direct(s Session, text string, owner bool) bool {
	return c.message(s, text, owner) == nil
}

func (c *Core) message(s Session, text string, owner bool) error {
	m, ok := c.harness(s.Harness).(Messenger)
	if !ok {
		return errNoMessenger
	}
	return m.Message(s, text, owner)
}

var errNoMessenger = errors.New("harness has no session inbox")
