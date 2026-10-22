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

// MarkAsking records that session's agent asked the owner in message, its own words kept whole; the session's waiting row shows it.
func (s *Store) MarkAsking(session, message string) error {
	_, err := s.db.Exec(`INSERT INTO ym_session_asks (session, text, at) VALUES (?, ?, ?)
		ON CONFLICT(session) DO UPDATE SET text = excluded.text, at = excluded.at`, session, message, now())
	return err
}

func (s *Store) asking() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT session, text FROM ym_session_asks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var session, text string
		if err := rows.Scan(&session, &text); err != nil {
			return nil, err
		}
		out[session] = text
	}
	return out, rows.Err()
}

// askLine is the line a waiting row shows for message: the first question the owner is asked, else its last line.
func (c *Core) askLine(message string) string {
	if c.Ask != nil {
		if line := c.Ask(message); line != "" {
			return line
		}
	}
	lines := strings.Split(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
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

// waiting lists idle sessions whose last reply asks the owner, with no open item, prompt or dismissal covering it, longest waiting first.
func (c *Core) waiting(b Board, asked []Need) ([]Need, error) {
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
		if _, managed := b.Org.Managed(key, b.parents); managed || covered[key] {
			continue
		}
		if text, ok := asking[key]; ok && !dismissed[key+"\x00"+replyKey(text)] {
			covered[key] = true
			out = append(out, Need{Session: s, Project: s.Project, Reply: text, Line: c.askLine(text)})
			continue
		}
		if c.Ask == nil || s.Status != Idle || s.Prompt != nil || s.Result == "" || dismissed[key+"\x00"+replyKey(s.Result)] {
			continue
		}
		line := c.Ask(s.Result)
		if line == "" {
			continue
		}
		covered[key] = true
		out = append(out, Need{Session: s, Project: s.Project, Reply: s.Result, Line: line})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Session.Since.Before(out[j].Session.Since) })
	return out, nil
}
