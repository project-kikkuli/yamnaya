package core

import "database/sql"

func (s *Store) AddSystemMessage(session, text string) (Message, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Message{}, err
	}
	defer tx.Rollback()
	seq, err := addSystemMessageIn(tx, session, text)
	if err != nil {
		return Message{}, err
	}
	if err = tx.Commit(); err != nil {
		return Message{}, err
	}
	return s.Message(seq)
}

func addSystemMessageIn(tx *sql.Tx, session, text string) (int64, error) {
	result, err := tx.Exec(`INSERT INTO ym_messages(session,text,state,created_at,source) VALUES(?,?,?,?, 'system')`, session, text, MsgPending, now())
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (c *Core) recordSystemRelayOutcome(r Relay, session, text string) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var reported bool
	if err = tx.QueryRow(`SELECT reported FROM ym_relay WHERE seq=?`, r.Seq).Scan(&reported); err != nil {
		return err
	}
	if reported {
		return nil
	}
	if _, err = addSystemMessageIn(tx, session, text); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE ym_relay SET reported=1 WHERE seq=?`, r.Seq); err != nil {
		return err
	}
	return tx.Commit()
}
