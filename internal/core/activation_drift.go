package core

import (
	"time"

	"github.com/project-kikkuli/yamnaya/internal/drift"
)

func driftNoticeKey(id string, sign drift.Sign) string {
	return "adrift\x00" + id + "\x00" + sign.Kind + "\x00" + sign.Since.UTC().Format(time.RFC3339Nano)
}

func agentEventGoal(b Board, session string) (string, bool) {
	manager, managed := b.Org.Managed(session, b.parents)
	if !managed {
		return "", false
	}
	if goal, ok := b.Org.Goals[manager.GoalID]; ok {
		return goal.ID, true
	}
	to := b.Org.Recipient(session, b.parents)
	return to, to != Owner
}

func (c *Core) driftActionNeeded(b Board, e GoalEvent) (bool, error) {
	for _, s := range b.Sessions {
		goal, managed := agentEventGoal(b, s.Key())
		if !managed || goal != e.Goal {
			continue
		}
		for _, sign := range s.Drift {
			if e.Key == driftNoticeKey(s.Key(), sign) {
				return true, nil
			}
		}
		for _, member := range s.Crew {
			for _, sign := range member.Drift {
				if e.Key == driftNoticeKey(s.Key()+"/"+member.ID, sign) {
					return true, nil
				}
			}
		}
	}
	return false, nil
}
