package core

import (
	"database/sql"
	"strings"
	"time"
)

const (
	// MsgPending waits for its session: typed in once a permission prompt clears, or read from ym item inbox.
	MsgPending = "pending"
	// MsgTyped was typed into the session's pane.
	MsgTyped = "typed"
	// MsgInbox was handed to the agent by ym item inbox.
	MsgInbox = "inbox"
	// MsgSent went straight to the session's own inbox.
	MsgSent = "sent"
)

// Message is a follow-up the owner sent to a session.
type Message struct {
	Seq       int64
	Session   string
	Text      string
	State     string
	CreatedAt time.Time
	Delivered time.Time
}

const messageSchema = `
CREATE TABLE IF NOT EXISTS ym_messages (
	seq INTEGER PRIMARY KEY AUTOINCREMENT,
	session TEXT NOT NULL,
	text TEXT NOT NULL,
	state TEXT NOT NULL,
	created_at TEXT NOT NULL,
	delivered_at TEXT
);
CREATE INDEX IF NOT EXISTS ym_messages_session ON ym_messages(session, state);
`

// AddMessage records a message to session in state; a typed or inboxed message is delivered now.
func (s *Store) AddMessage(session, text, state string) (Message, error) {
	at := now()
	var delivered any
	if state != MsgPending {
		delivered = at
	}
	res, err := s.db.Exec(`INSERT INTO ym_messages (session, text, state, created_at, delivered_at) VALUES (?, ?, ?, ?, ?)`, session, text, state, at, delivered)
	if err != nil {
		return Message{}, err
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return Message{}, err
	}
	return s.Message(seq)
}

func (s *Store) Message(seq int64) (Message, error) {
	ms, err := s.messages(`WHERE seq = ?`, seq)
	if err != nil {
		return Message{}, err
	}
	if len(ms) == 0 {
		return Message{}, ErrNotFound
	}
	return ms[0], nil
}

// Messages lists the messages sent to any of sessions, oldest first.
func (s *Store) Messages(sessions ...string) ([]Message, error) {
	if len(sessions) == 0 {
		return nil, nil
	}
	args := make([]any, len(sessions))
	for i, id := range sessions {
		args[i] = id
	}
	return s.messages(`WHERE session IN (?`+strings.Repeat(",?", len(sessions)-1)+`)`, args...)
}

func (s *Store) messages(where string, args ...any) ([]Message, error) {
	rows, err := s.db.Query(`SELECT seq, session, text, state, created_at, delivered_at FROM ym_messages `+where+` ORDER BY seq`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var created, delivered sql.NullString
		if err := rows.Scan(&m.Seq, &m.Session, &m.Text, &m.State, &created, &delivered); err != nil {
			return nil, err
		}
		m.CreatedAt, m.Delivered = parseTime(created), parseTime(delivered)
		out = append(out, m)
	}
	return out, rows.Err()
}

// Claim moves a pending message to state; false when another delivery took it first.
func (s *Store) Claim(seq int64, state string) (bool, error) {
	res, err := s.db.Exec(`UPDATE ym_messages SET state = ?, delivered_at = ? WHERE seq = ? AND state = ?`, state, now(), seq, MsgPending)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// Unclaim returns a message whose delivery failed to pending.
func (s *Store) Unclaim(seq int64) error {
	_, err := s.db.Exec(`UPDATE ym_messages SET state = ?, delivered_at = NULL WHERE seq = ?`, MsgPending, seq)
	return err
}

// PendingMessages lists every message still waiting for its session, oldest first.
func (s *Store) PendingMessages() ([]Message, error) {
	return s.messages(`WHERE state = ?`, MsgPending)
}

func (c *Core) attachMessages(sessions []Live, moved map[string]string) error {
	owner := map[string]string{}
	var ids []string
	for _, s := range sessions {
		if s.ID != "" {
			ids = append(ids, s.ID)
			owner[s.ID] = s.ID
		}
	}
	for old, now := range moved {
		ids = append(ids, old)
		owner[old] = now
	}
	ms, err := c.Store.Messages(ids...)
	if err != nil {
		return err
	}
	by := map[string][]Message{}
	for _, m := range ms {
		by[owner[m.Session]] = append(by[owner[m.Session]], m)
	}
	for i := range sessions {
		sessions[i].Messages = by[sessions[i].ID]
	}
	return nil
}
