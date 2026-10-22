package core

import (
	"database/sql"
	"strconv"
)

var retiredTables = []string{
	"ym_work", "ym_work_events", "ym_work_outbox", "ym_work_seen", "ym_work_peers", "ym_work_commands",
	"ym_work_source_errors", "ym_work_polls", "ym_work_notifications", "ym_work_runs",
	"ym_agents", "ym_attachments", "ym_agent_rules", "ym_agent_queue", "ym_agent_checkpoints",
	"ym_agent_settings", "ym_agent_notifications", "ym_agent_batches", "ym_agent_recipients",
	"items", "item_events", "projects", "windows", "staged",
}

const retiredHandoffReason = "auto-extracted handoff retired with the old work layer"

// retireWork drops the work and agent layer's tables and dismisses the handoff items it left open, once.
func retireWork(db *sql.DB) error {
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='ym_work'`).Scan(&has); err != nil || has == 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT seq FROM ym_items WHERE state = ? AND seq IN (SELECT item FROM ym_handoff_items)`, ItemOpen)
	if err != nil {
		return err
	}
	var open []int64
	for rows.Next() {
		var seq int64
		if err = rows.Scan(&seq); err != nil {
			rows.Close()
			return err
		}
		open = append(open, seq)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	at := now()
	for _, seq := range open {
		if _, err = tx.Exec(`UPDATE ym_items SET state = ?, reason = ?, answered_at = ?, delivered_at = ? WHERE seq = ?`, ItemDismissed, retiredHandoffReason, at, at, seq); err != nil {
			return err
		}
		if err = addEvent(tx, "#"+strconv.FormatInt(seq, 10), ItemOpen, ItemDismissed, retiredHandoffReason); err != nil {
			return err
		}
	}
	for _, t := range retiredTables {
		if _, err = tx.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return err
		}
	}
	return tx.Commit()
}
