package core

import (
	"strings"
	"time"
)

const forwardSchema = `
CREATE TABLE IF NOT EXISTS ym_forwarded (
	id TEXT PRIMARY KEY,
	at TEXT NOT NULL
) WITHOUT ROWID;
`

// ForwardIdle records the last reply of every idle agent a manager runs, once per reply, as an event that wakes the manager; an agent
// of a chief is handed to it directly.
func (c *Core) ForwardIdle(b Board) error {
	for i := range b.Sessions {
		s := &b.Sessions[i]
		key := s.Key()
		if s.Status != Idle || s.Prompt != nil || s.Result == "" {
			continue
		}
		m, managed := b.Org.Managed(key, b.parents)
		if !managed {
			continue
		}
		if _, isManager := b.Org.ManagerOf(key); isManager {
			continue
		}
		id := key + "\x00" + s.ResultAt.UTC().Format(time.RFC3339Nano) + "\x00" + replyKey(s.Result)
		var seen int
		if err := c.Store.db.QueryRow(`SELECT count(*) FROM ym_forwarded WHERE id = ?`, id).Scan(&seen); err != nil {
			return err
		}
		if seen > 0 {
			continue
		}
		if g, ok := b.Org.Goals[m.GoalID]; ok {
			if _, _, err := c.RecordGoalEvent(g.ID, EventFinished, strings.TrimPrefix(idleText(*s), "ym: "), ""); err != nil {
				return err
			}
		} else if to := b.Org.Recipient(key, b.parents); to != Owner {
			if err := c.tellManager(b, b.Org, to, idleText(*s)); err != nil {
				return err
			}
		}
		if _, err := c.Store.db.Exec(`INSERT INTO ym_forwarded (id, at) VALUES (?, ?)`, id, now()); err != nil {
			return err
		}
	}
	return nil
}

// idleText is how an idle agent's last reply reaches its manager.
func idleText(s Live) string {
	from := "agent " + Short(s.Key())
	if s.Title != "" {
		from += " (" + oneLine(s.Title) + ")"
	}
	return "ym: " + from + " is idle. Its last message, verbatim: " + strings.TrimSpace(s.Result)
}
