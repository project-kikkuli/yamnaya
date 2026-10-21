package core

import "database/sql"

func prMergeDetails(db *sql.DB) error {
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_prs') WHERE name='merge_details'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`ALTER TABLE ym_prs ADD COLUMN merge_details BLOB NOT NULL DEFAULT '{}'`); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE ym_prs SET fetched_at=NULL WHERE state='OPEN'`); err != nil {
		return err
	}
	return tx.Commit()
}
