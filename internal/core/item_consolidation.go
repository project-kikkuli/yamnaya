package core

import (
	"database/sql"
	"errors"
	"fmt"
)

const itemConsolidationSchema = `
CREATE TABLE IF NOT EXISTS ym_item_consolidations (
	copy INTEGER PRIMARY KEY REFERENCES ym_items(seq),
	source INTEGER NOT NULL REFERENCES ym_items(seq),
	created_at TEXT NOT NULL,
	CHECK(copy != source)
);
CREATE INDEX IF NOT EXISTS ym_item_consolidations_source ON ym_item_consolidations(source);
`

func consolidatedInto(tx *sql.Tx, seq int64) (int64, error) {
	var source int64
	err := tx.QueryRow(`SELECT source FROM ym_item_consolidations WHERE copy = ?`, seq).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return source, err
}

func resolveConsolidatedItems(tx *sql.Tx, source Item, to, answer, reason string, at *string) error {
	if to == ItemOpen {
		_, err := tx.Exec(`UPDATE ym_items SET delivered_at = ? WHERE seq IN (SELECT copy FROM ym_item_consolidations WHERE source = ?)`, now(), source.Seq)
		return err
	}
	if to != ItemAnswered && to != ItemDismissed {
		return nil
	}
	rows, err := tx.Query(`SELECT i.seq, i.state FROM ym_items i JOIN ym_item_consolidations c ON c.copy = i.seq WHERE c.source = ?`, source.Seq)
	if err != nil {
		return err
	}
	type copyItem struct {
		seq   int64
		state string
	}
	var copies []copyItem
	for rows.Next() {
		var item copyItem
		if err := rows.Scan(&item.seq, &item.state); err != nil {
			rows.Close()
			return err
		}
		copies = append(copies, item)
	}
	if err := errors.Join(rows.Err(), rows.Close()); err != nil {
		return err
	}
	for _, copy := range copies {
		if _, err := tx.Exec(`UPDATE ym_items SET state = ?, answer = ?, reason = ?, answered_at = ?, delivered_at = NULL, held_at = NULL WHERE seq = ?`,
			to, answer, reason, at, copy.seq); err != nil {
			return err
		}
		if err := addEvent(tx, fmt.Sprintf("#%d", copy.seq), copy.state, to, "resolved through "+source.Key()+": "+answer+reason); err != nil {
			return err
		}
	}
	return nil
}
