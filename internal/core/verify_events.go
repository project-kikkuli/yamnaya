package core

const (
	GoalEventVerifying    = "verifying"
	GoalEventVerified     = "verified"
	GoalEventFailedVerify = "failed-verify"
	GoalEventStuck        = "stuck"
)

func (c *Core) goalEvent(g Goal, kind, payload string) error { return nil }
