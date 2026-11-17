package core

import (
	"fmt"
	"time"
)

func swollenNoticeKey(at time.Time, s Swollen) string {
	started := at.Add(-s.Elapsed).Truncate(time.Minute).UTC().Format(time.RFC3339)
	return fmt.Sprintf("swollen\x00%d\x00%s", s.PID, started)
}

func (c *Core) swollenActionNeeded(b Board, e GoalEvent) (bool, error) {
	for _, s := range b.Machine.Swollen {
		if s.Session == "" || s.PID <= 0 || e.Key != swollenNoticeKey(b.At, s) {
			continue
		}
		if !e.Created.IsZero() && b.At.Add(-s.Elapsed).After(e.Created) {
			continue
		}
		goal := s.Goal
		if goal == "" {
			m, managed := b.Org.Managed(s.Session, b.parents)
			if !managed {
				continue
			}
			goal = m.GoalID
			if goal == "" {
				goal = m.ID
			}
		}
		if goal == e.Goal {
			return true, nil
		}
	}
	return false, nil
}
