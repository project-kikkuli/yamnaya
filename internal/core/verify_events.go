package core

const (
	GoalEventVerifying    = EventVerifying
	GoalEventVerified     = EventVerified
	GoalEventFailedVerify = EventFailedVerify
	GoalEventStuck        = EventStuck
)

// goalEvent records a verification event the manager was told of directly; a pass also records the goal done for the owner.
func (c *Core) goalEvent(g Goal, kind, payload string) error {
	now := c.now()
	if _, _, err := c.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: kind, Payload: payload, Created: now, Delivered: now}); err != nil || kind != EventVerified {
		return err
	}
	_, _, err := c.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventDone, Payload: g.Summary, Created: now, Delivered: now})
	return err
}
