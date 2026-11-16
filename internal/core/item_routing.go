package core

import "fmt"

func (s *Store) rerouteItem(it Item, recipient, reason string) (bool, error) {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE ym_items SET recipient = ?, preparer = '' WHERE seq = ? AND recipient = ? AND state = ?`, recipient, it.Seq, it.Recipient, it.State)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if err := addEvent(tx, it.Key(), it.State, it.State, fmt.Sprintf("routed from %q to %q: %s", it.Recipient, recipient, reason)); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func (c *Core) answeringOrg(b Board, o Org) Org {
	available := o
	available.Managers = make(map[string]Manager, len(o.Managers))
	for id, m := range o.Managers {
		_, _, remote := SplitRemote(m.Session)
		starting := !m.LaunchedAt.IsZero() && c.now().Sub(m.LaunchedAt) < StartGrace && m.LastError == ""
		if !remote && m.Host == "" && !b.Alive(m) && !starting {
			m.Session = ""
		}
		available.Managers[id] = m
	}
	return available
}
