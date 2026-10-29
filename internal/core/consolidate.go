package core

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

func (c *Core) Consolidate(copyRef, sourceRef, why string) (Item, error) {
	if strings.TrimSpace(why) == "" {
		return Item{}, fmt.Errorf("consolidating requires a reason")
	}
	copyItem, err := c.Store.Item(copyRef)
	if err != nil {
		return Item{}, err
	}
	source, err := c.Store.Item(sourceRef)
	if err != nil {
		return Item{}, err
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return Item{}, err
	}
	defer tx.Rollback()
	copyItem, err = scanItem(tx.QueryRow(`SELECT `+itemCols+` FROM ym_items WHERE seq = ?`, copyItem.Seq))
	if err != nil {
		return Item{}, err
	}
	source, err = scanItem(tx.QueryRow(`SELECT `+itemCols+` FROM ym_items WHERE seq = ?`, source.Seq))
	if err != nil {
		return Item{}, err
	}
	if copyItem.Seq == source.Seq || copyItem.Kind != source.Kind || copyItem.State != ItemOpen {
		return Item{}, fmt.Errorf("choose a distinct open copy of the same item kind")
	}
	if copyItem.Rec != "" {
		source.Rec = copyItem.Rec
	}
	if len(copyItem.Options) > 0 {
		source.Options = copyItem.Options
	}
	for _, ev := range copyItem.Evidence {
		if !slices.Contains(source.Evidence, ev) {
			source.Evidence = append(source.Evidence, ev)
		}
	}
	if copyItem.Tried != "" && copyItem.Tried != source.Tried {
		source.Tried = strings.TrimSpace(source.Tried + "\n" + copyItem.Tried)
	}
	source.Blocks = max(source.Blocks, copyItem.Blocks)
	if err := ready(source); err != nil {
		return Item{}, err
	}
	options, err := json.Marshal(source.Options)
	if err != nil {
		return Item{}, err
	}
	evidence, err := json.Marshal(source.Evidence)
	if err != nil {
		return Item{}, err
	}
	if _, err := tx.Exec(`UPDATE ym_items SET state='open', recipient='', preparer='', answer='', reason='', answered_at=NULL, delivered_at=NULL, held_at=NULL, rec=?, options=?, evidence=?, tried=?, blocks=? WHERE seq=?`, source.Rec, string(options), string(evidence), source.Tried, source.Blocks, source.Seq); err != nil {
		return Item{}, err
	}
	reason := "consolidated into " + source.Qualified() + ": " + why
	if _, err := tx.Exec(`UPDATE ym_items SET state='dismissed', reason=?, answered_at=?, delivered_at=NULL, held_at=NULL WHERE seq=?`, reason, stamp(c.now()), copyItem.Seq); err != nil {
		return Item{}, err
	}
	if err := addEvent(tx, source.Key(), source.State, ItemOpen, "consolidated "+copyItem.Qualified()+": "+why); err != nil {
		return Item{}, err
	}
	if err := addEvent(tx, copyItem.Key(), ItemOpen, ItemDismissed, reason); err != nil {
		return Item{}, err
	}
	if err := tx.Commit(); err != nil {
		return Item{}, err
	}
	c.Store.invalidate()
	return c.Store.Item(source.Key())
}
