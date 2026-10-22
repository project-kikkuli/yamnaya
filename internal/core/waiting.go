package core

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
)

const waitingSchema = `
CREATE TABLE IF NOT EXISTS ym_waiting_dismissed (
	session TEXT NOT NULL,
	reply TEXT NOT NULL,
	at TEXT NOT NULL,
	PRIMARY KEY (session, reply)
) WITHOUT ROWID;
`

// Waiting reports whether the need is an idle session whose last reply asks the owner.
func (n Need) Waiting() bool { return n.Item == nil && n.Reply != "" }

func replyKey(reply string) string {
	h := sha1.Sum([]byte(strings.TrimSpace(reply)))
	return hex.EncodeToString(h[:12])
}

// DismissWaiting hides session's waiting row for reply; a later reply that asks shows again.
func (s *Store) DismissWaiting(session, reply string) error {
	if _, err := s.db.Exec(`DELETE FROM ym_session_asks WHERE session = ?`, session); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO ym_waiting_dismissed (session, reply, at) VALUES (?, ?, ?)`, session, replyKey(reply), now())
	return err
}

func (s *Store) dismissedWaiting() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT session, reply FROM ym_waiting_dismissed`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var session, reply string
		if err := rows.Scan(&session, &reply); err != nil {
			return nil, err
		}
		out[session+"\x00"+reply] = true
	}
	return out, rows.Err()
}

// AnswerWaiting sends text to s as the owner's message and hides the row for the reply it answers.
func (c *Core) AnswerWaiting(s Live, reply, text string) (string, error) {
	msg, err := c.Launches.Send(s, text)
	if err != nil {
		return msg, err
	}
	return msg, c.Store.DismissWaiting(s.Key(), reply)
}

// waiting lists idle sessions whose last reply asks the owner, or follows one since the owner last wrote that did, with no open item, prompt or dismissal covering it, longest waiting first.
func (c *Core) waiting(b Board, asked []Need) ([]Need, error) {
	if c.Ask == nil {
		return nil, nil
	}
	dismissed, err := c.Store.dismissedWaiting()
	if err != nil {
		return nil, err
	}
	asking, err := c.Store.asking()
	if err != nil {
		return nil, err
	}
	covered := map[string]bool{}
	for _, n := range asked {
		if n.Session != nil {
			covered[n.Session.Key()] = true
		}
	}
	for _, p := range b.Projects {
		for _, it := range p.Items {
			if it.State != ItemOpen {
				continue
			}
			covered[it.Session] = true
			if next, ok := b.moved[it.Session]; ok {
				covered[next] = true
			}
		}
	}
	var out []Need
	for i := range b.Sessions {
		s := &b.Sessions[i]
		key := s.Key()
		if _, managed := b.Org.Managed(key, b.parents); managed {
			continue
		}
		if covered[key] {
			continue
		}
		if text, ok := asking[key]; ok && !dismissed[key+"\x00"+replyKey(text)] {
			covered[key] = true
			out = append(out, Need{Session: s, Project: s.Project, Reply: text, Line: c.askLine(text)})
			continue
		}
		if s.Status != Idle || s.Prompt != nil || s.Result == "" {
			continue
		}
		reply, line := s.Result, c.Ask(s.Result)
		if line == "" && s.Asked != "" {
			reply, line = s.Asked, c.Ask(s.Asked)
		}
		if line == "" || dismissed[key+"\x00"+replyKey(reply)] {
			continue
		}
		covered[key] = true
		out = append(out, Need{Session: s, Project: s.Project, Reply: reply, Line: line})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Session.Since.Before(out[j].Session.Since) })
	return out, nil
}
