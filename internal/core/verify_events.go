package core

// goalEvent records a verification event on g; one the manager was not told of directly reaches it in its next wake.
func (c *Core) goalEvent(g Goal, kind, payload string, told bool) error {
	e := GoalEvent{Goal: g.ID, Kind: kind, Payload: payload, Created: c.now()}
	if told {
		e.Delivered = e.Created
	}
	_, _, err := c.Store.AddGoalEvent(e)
	return err
}
