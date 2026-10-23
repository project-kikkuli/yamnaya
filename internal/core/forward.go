package core

import (
	"strings"
	"sync"
	"time"
)

const forwardSchema = `
CREATE TABLE IF NOT EXISTS ym_forwarded (
	id TEXT PRIMARY KEY,
	at TEXT NOT NULL
) WITHOUT ROWID;
CREATE TABLE IF NOT EXISTS ym_agent_reports (
	session TEXT PRIMARY KEY,
	head TEXT NOT NULL DEFAULT '',
	said TEXT NOT NULL DEFAULT '',
	at TEXT NOT NULL
) WITHOUT ROWID;
`

// ForwardIdle records the last reply of every idle agent a manager or chief runs, once per reply, as an event in its inbox; a reply
// that adds nothing is recorded as idle-again, which ym only counts.
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
		if c.fw.has(id) {
			continue
		}
		var seen int
		if err := c.Store.db.QueryRow(`SELECT count(*) FROM ym_forwarded WHERE id = ?`, id).Scan(&seen); err != nil {
			return err
		}
		if seen > 0 {
			c.fw.add(id)
			continue
		}
		fresh, err := c.news(*s)
		if err != nil {
			return err
		}
		kind := EventFinished
		if !fresh {
			kind = EventIdleAgain
		}
		text := strings.TrimPrefix(idleText(*s), "ym: ")
		inbox := m.GoalID
		if _, ok := b.Org.Goals[inbox]; !ok {
			inbox = b.Org.Recipient(key, b.parents)
		}
		if _, _, remote := SplitRemote(inbox); remote && fresh {
			if err := c.tellManager(b, b.Org, inbox, "ym: "+clip(text, lineMax)); err != nil {
				return err
			}
		} else if inbox != Owner && !remote {
			if _, _, err := c.RecordGoalEvent(inbox, kind, text, ""); err != nil {
				return err
			}
		}
		if _, err := c.Store.db.Exec(`INSERT INTO ym_forwarded (id, at) VALUES (?, ?)`, id, now()); err != nil {
			return err
		}
		c.fw.add(id)
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

// forwardedSet remembers replies known forwarded, so a reply already handed on costs no query on later cycles.
type forwardedSet struct {
	mu  sync.Mutex
	ids map[string]bool
}

func (f *forwardedSet) has(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ids[id]
}

func (f *forwardedSet) add(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ids == nil {
		f.ids = map[string]bool{}
	}
	f.ids[id] = true
}
