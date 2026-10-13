package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Stage records the agent pane lent to the ym home stage: the window it belongs to, the tmux window it left, and its pane id.
type Stage struct {
	WindowID int64
	Origin   string
	Pane     string
}

func (s *Store) Stage() (Stage, bool, error) {
	var st Stage
	err := s.db.QueryRow(`SELECT window_id, origin, pane FROM stage WHERE slot = 1`).Scan(&st.WindowID, &st.Origin, &st.Pane)
	if errors.Is(err, sql.ErrNoRows) {
		return st, false, nil
	}
	return st, err == nil, err
}

func (s *Store) SetStage(st Stage) error {
	_, err := s.db.Exec(`INSERT INTO stage (slot, window_id, origin, pane) VALUES (1, ?, ?, ?)
		ON CONFLICT (slot) DO UPDATE SET window_id = excluded.window_id, origin = excluded.origin, pane = excluded.pane`,
		st.WindowID, st.Origin, st.Pane)
	return err
}

func (s *Store) ClearStage() error {
	_, err := s.db.Exec(`DELETE FROM stage`)
	return err
}

func (s *Store) WindowByID(id int64) (Window, error) {
	w, err := scanWindow(s.db.QueryRow(`SELECT `+windowCols+` FROM windows WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return w, fmt.Errorf("window %d: %w", id, ErrNotFound)
	}
	return w, err
}

func (s *Store) SetTarget(windowID int64, target string) error {
	_, err := s.db.Exec(`UPDATE windows SET tmux_target = ? WHERE id = ?`, target, windowID)
	return err
}
