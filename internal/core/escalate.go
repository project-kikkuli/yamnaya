package core

import "fmt"

func (c *Core) Escalate(ref string) (Item, error) {
	if it, _, remote, err := c.remoteItem(ref, "escalate"); remote {
		return it, err
	}
	original, err := c.Store.Item(ref)
	if err != nil {
		return original, err
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	it, err := scanItem(tx.QueryRow(`SELECT `+itemCols+` FROM ym_items WHERE seq = ?`, original.Seq))
	if err != nil {
		return it, err
	}
	if it.State != ItemOpen {
		return it, fmt.Errorf("item %s is %s; only an open ask can be escalated", it.ID, it.State)
	}
	if err := ready(it); err != nil {
		return it, fmt.Errorf("prepare %s with ym item prepare before escalating: %w", it.Key(), err)
	}
	if it.Recipient == Owner {
		return it, nil
	}
	if _, err := tx.Exec(`UPDATE ym_items SET recipient = '', preparer = '' WHERE seq = ?`, it.Seq); err != nil {
		return it, err
	}
	if err := addEvent(tx, it.Key(), ItemOpen, ItemOpen, "escalated to owner from "+it.Recipient); err != nil {
		return it, err
	}
	if err := tx.Commit(); err != nil {
		return it, err
	}
	return c.Store.Item(it.Key())
}
