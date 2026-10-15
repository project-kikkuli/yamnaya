package core

// Resolve follows a session's continued-in records to the session that carries its conversation now.
func (c *Core) Resolve(id string) string {
	seen := map[string]bool{id: true}
	for {
		next := c.successor(id)
		if next == "" || seen[next] {
			return id
		}
		seen[next] = true
		id = next
	}
}

func (c *Core) successor(id string) string {
	for _, h := range c.Harnesses {
		if ct, ok := h.(Continuer); ok {
			if next := ct.Successor(id); next != "" {
				return next
			}
		}
	}
	return ""
}

// carries reports, per session id, whether its conversation now runs as session.
func (c *Core) carries(session string) func(id string) bool {
	now := c.Resolve(session)
	known := map[string]bool{session: true, now: true}
	return func(id string) bool {
		got, ok := known[id]
		if !ok {
			got = c.Resolve(id) == now
			known[id] = got
		}
		return got
	}
}

// Direct hands text to a running session's own inbox; false when its harness has none or the inbox did not take it.
func (c *Core) Direct(s Session, text string) bool {
	m, ok := c.harness(s.Harness).(Messenger)
	return ok && m.Message(s, text) == nil
}
