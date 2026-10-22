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

// ForwardIdle hands the last reply of every idle agent a manager runs to the manager or chief its asks go to, once per reply.
func (c *Core) ForwardIdle(b Board) error {
	for i := range b.Sessions {
		s := &b.Sessions[i]
		key := s.Key()
		if s.Status != Idle || s.Prompt != nil || s.Result == "" {
			continue
		}
		if _, managed := b.Org.Managed(key, b.parents); !managed {
			continue
		}
		if _, isManager := b.Org.ManagerOf(key); isManager {
			continue
		}
		to := b.Org.Recipient(key, b.parents)
		if to == Owner {
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
		if err := c.tell(b, b.Org.Managers[to].Session, idleText(*s), false); err != nil {
			return err
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
