package core

import (
	"database/sql"
	"strconv"
	"strings"
	"time"
)

const askedReason = "handoff from a session you started; its waiting row shows the agent's message"

// sessionAsks creates the table of sessions whose agent asked the owner, once, turning each open item the handoff catcher filed for
// the owner into its session's waiting row: the item is dismissed without a notice and the row keeps the agent's message whole.
func sessionAsks(db *sql.DB) error {
	var has, handoffs int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='ym_session_asks'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='ym_handoff_claims'`).Scan(&handoffs); err != nil {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`CREATE TABLE ym_session_asks (session TEXT PRIMARY KEY, text TEXT NOT NULL, at TEXT NOT NULL)`); err != nil {
		return err
	}
	if handoffs == 0 {
		return tx.Commit()
	}
	rows, err := tx.Query(`SELECT i.seq, i.session, COALESCE((SELECT json_extract(CAST(c.claim AS TEXT), '$.Event.Text') FROM ym_handoff_targets t
		JOIN ym_handoff_claims c ON c.id = t.id WHERE t.target = 'item:#' || i.seq LIMIT 1), i.question)
		FROM ym_items i WHERE i.state = ? AND i.recipient = '' AND i.seq IN (SELECT item FROM ym_handoff_items) ORDER BY i.seq`, ItemOpen)
	if err != nil {
		return err
	}
	type open struct {
		seq           int64
		session, text string
	}
	var all []open
	for rows.Next() {
		var o open
		if err = rows.Scan(&o.seq, &o.session, &o.text); err != nil {
			rows.Close()
			return err
		}
		all = append(all, o)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	at := now()
	for _, o := range all {
		if _, err = tx.Exec(`UPDATE ym_items SET state = ?, reason = ?, answered_at = ?, delivered_at = ? WHERE seq = ?`, ItemDismissed, askedReason, at, at, o.seq); err != nil {
			return err
		}
		if err = addEvent(tx, "#"+strconv.FormatInt(o.seq, 10), ItemOpen, ItemDismissed, askedReason); err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO ym_session_asks (session, text, at) VALUES (?, ?, ?) ON CONFLICT(session) DO UPDATE SET text = excluded.text`, o.session, o.text, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type sessionAsk struct {
	text string
	at   time.Time
}

func (s *Store) asking() (map[string]sessionAsk, error) {
	rows, err := s.db.Query(`SELECT session, text, at FROM ym_session_asks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]sessionAsk{}
	for rows.Next() {
		var session, text, at string
		if err := rows.Scan(&session, &text, &at); err != nil {
			return nil, err
		}
		t, _ := time.Parse(time.RFC3339Nano, at)
		out[session] = sessionAsk{text, t}
	}
	return out, rows.Err()
}

// askLine is the line a waiting row shows for message: the first question the owner is asked, else its last line.
func (c *Core) askLine(message string) string {
	if line := c.Ask(message); line != "" {
		return line
	}
	lines := strings.Split(strings.TrimSpace(message), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
