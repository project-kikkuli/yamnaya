package core

import (
	"database/sql"
	"strconv"
)

var handoffTables = []string{"ym_handoff_files", "ym_handoff_events", "ym_handoff_claims", "ym_handoff_items", "ym_handoff_targets"}

const retiredHandoffReason = "auto-extracted handoff retired with the transcript classifier"

// retireHandoffs dismisses the open and snoozed items the transcript classifier filed and drops its tables, once.
func retireHandoffs(db *sql.DB) error {
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='ym_handoff_items'`).Scan(&has); err != nil || has == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT seq, state FROM ym_items WHERE state IN (?, ?) AND seq IN (SELECT item FROM ym_handoff_items)`, ItemOpen, ItemSnoozed)
	if err != nil {
		return err
	}
	type pending struct {
		seq   int64
		state string
	}
	var retire []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.seq, &p.state); err != nil {
			rows.Close()
			return err
		}
		retire = append(retire, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	at := now()
	for _, p := range retire {
		if _, err = tx.Exec(`UPDATE ym_items SET state = ?, reason = ?, answered_at = ?, delivered_at = ? WHERE seq = ?`, ItemDismissed, retiredHandoffReason, at, at, p.seq); err != nil {
			return err
		}
		if err = addEvent(tx, "#"+strconv.FormatInt(p.seq, 10), p.state, ItemDismissed, retiredHandoffReason); err != nil {
			return err
		}
	}
	for _, t := range handoffTables {
		if _, err = tx.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return err
		}
	}
	return tx.Commit()
}
