package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// ReviveWindows returns the windows a revive restores: open ones for an active project, those closed by landing otherwise.
func (s *Store) ReviveWindows(name string) (Project, []Window, error) {
	p, err := s.Project(name)
	if err != nil {
		return p, nil, err
	}
	if p.State == ProjectActive {
		ws, err := s.OpenWindows(name)
		return p, ws, err
	}
	rows, err := s.db.Query(`SELECT `+windowCols+` FROM windows WHERE project = ? AND closed_at = (SELECT landed_at FROM projects WHERE name = ?) ORDER BY id`, name, name)
	if err != nil {
		return p, nil, err
	}
	defer rows.Close()
	var out []Window
	for rows.Next() {
		w, err := scanWindow(rows)
		if err != nil {
			return p, nil, err
		}
		out = append(out, w)
	}
	return p, out, rows.Err()
}

// Revive marks the project active and reopens windows, calling launch for each before commit.
func (s *Store) Revive(name string, windows []Window, launch func(Window) (string, Window, error)) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT state FROM projects WHERE name = ?`, name).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project %q: %w", name, ErrNotFound)
		}
		return err
	}
	if _, err := tx.Exec(`UPDATE projects SET state = ?, landed_at = NULL WHERE name = ?`, ProjectActive, name); err != nil {
		return err
	}
	for _, w := range windows {
		target, got, err := launch(w)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE windows SET closed_at = NULL, tmux_target = ?, session_id = ?, prompt = ? WHERE id = ?`,
			target, got.SessionID, got.Prompt, w.ID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SwitchHarness points an open window at a new harness and session.
func (s *Store) SwitchHarness(windowID int64, harness, sessionID string) error {
	res, err := s.db.Exec(`UPDATE windows SET harness = ?, session_id = ? WHERE id = ? AND closed_at IS NULL`, harness, sessionID, windowID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("window %d: %w", windowID, ErrNotFound)
	}
	return nil
}
