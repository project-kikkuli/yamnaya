package store

import (
	"database/sql"
	"errors"
	"fmt"
)

// Stage records the pane lent to the ym home stage: the ym window it belongs to (0 for a discovered session), the tmux window and session it left, and its pane id.
type Stage struct {
	WindowID      int64
	Origin        string
	OriginSession string
	Pane          string
}

func (s *Store) Stage() (Stage, bool, error) {
	var st Stage
	var id sql.NullInt64
	err := s.db.QueryRow(`SELECT window_id, origin, origin_session, pane FROM staged WHERE slot = 1`).Scan(&id, &st.Origin, &st.OriginSession, &st.Pane)
	st.WindowID = id.Int64
	if errors.Is(err, sql.ErrNoRows) {
		return st, false, nil
	}
	return st, err == nil, err
}

func (s *Store) SetStage(st Stage) error {
	var id any
	if st.WindowID != 0 {
		id = st.WindowID
	}
	_, err := s.db.Exec(`INSERT INTO staged (slot, window_id, origin, origin_session, pane) VALUES (1, ?, ?, ?, ?)
		ON CONFLICT (slot) DO UPDATE SET window_id = excluded.window_id, origin = excluded.origin, origin_session = excluded.origin_session, pane = excluded.pane`,
		id, st.Origin, st.OriginSession, st.Pane)
	return err
}

func (s *Store) ClearStage() error {
	_, err := s.db.Exec(`DELETE FROM staged`)
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
