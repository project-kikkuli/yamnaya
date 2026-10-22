package core

import "database/sql"

var retiredTables = []string{
	"ym_work", "ym_work_events", "ym_work_outbox", "ym_work_seen", "ym_work_peers", "ym_work_commands",
	"ym_work_source_errors", "ym_work_polls", "ym_work_notifications", "ym_work_runs",
	"ym_agents", "ym_attachments", "ym_agent_rules", "ym_agent_queue", "ym_agent_checkpoints",
	"ym_agent_settings", "ym_agent_notifications", "ym_agent_batches", "ym_agent_recipients",
	"items", "item_events", "projects", "windows", "staged",
}

// retireWork drops the work and agent layer's tables, once.
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
	for _, t := range retiredTables {
		if _, err = tx.Exec(`DROP TABLE IF EXISTS ` + t); err != nil {
			return err
		}
	}
	return tx.Commit()
}
