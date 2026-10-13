package store

import (
	"database/sql"
	"fmt"
	"time"
)

const (
	RoleManager = "manager"
	RoleWorker  = "worker"
)

type Window struct {
	ID         int64
	Project    string
	Name       string
	Role       string
	Harness    string
	SessionID  string
	Prompt     string
	TmuxTarget string
	Managed    bool
	CreatedAt  time.Time
	ClosedAt   time.Time
}

const windowCols = `id, project, name, role, harness, session_id, prompt, tmux_target, managed, created_at, closed_at`

func scanWindow(row interface{ Scan(...any) error }) (Window, error) {
	var w Window
	var created, closed sql.NullString
	err := row.Scan(&w.ID, &w.Project, &w.Name, &w.Role, &w.Harness, &w.SessionID, &w.Prompt, &w.TmuxTarget, &w.Managed, &created, &closed)
	w.CreatedAt = parseTime(created)
	w.ClosedAt = parseTime(closed)
	return w, err
}

func insertWindow(tx *sql.Tx, w Window, launch func(Window) (string, error)) error {
	var taken int
	if err := tx.QueryRow(`SELECT count(*) FROM windows WHERE project = ? AND name = ? AND closed_at IS NULL`, w.Project, w.Name).Scan(&taken); err != nil {
		return err
	}
	if taken > 0 {
		return fmt.Errorf("window %q already exists in project %q", w.Name, w.Project)
	}
	if w.CreatedAt.IsZero() {
		w.CreatedAt = time.Now().UTC()
	}
	res, err := tx.Exec(`INSERT INTO windows (project, name, role, harness, session_id, prompt, managed, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.Project, w.Name, w.Role, w.Harness, w.SessionID, w.Prompt, w.Managed, w.CreatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if w.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	target, err := launch(w)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`UPDATE windows SET tmux_target = ? WHERE id = ?`, target, w.ID)
	return err
}

// AddWindow records a window in an active project, calling launch before commit so a failed launch leaves no row.
func (s *Store) AddWindow(w Window, launch func(Window) (string, error)) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var state string
	if err := tx.QueryRow(`SELECT state FROM projects WHERE name = ?`, w.Project).Scan(&state); err != nil {
		if err == sql.ErrNoRows {
			return fmt.Errorf("project %q: %w", w.Project, ErrNotFound)
		}
		return err
	}
	if state != ProjectActive {
		return fmt.Errorf("project %q is landed", w.Project)
	}
	if err := insertWindow(tx, w, launch); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) OpenWindows(project string) ([]Window, error) {
	rows, err := s.db.Query(`SELECT `+windowCols+` FROM windows WHERE project = ? AND closed_at IS NULL ORDER BY id`, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Window
	for rows.Next() {
		w, err := scanWindow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) OpenWindow(project, name string) (Window, error) {
	w, err := scanWindow(s.db.QueryRow(`SELECT `+windowCols+` FROM windows WHERE project = ? AND name = ? AND closed_at IS NULL`, project, name))
	if err == sql.ErrNoRows {
		return w, fmt.Errorf("window %q in project %q: %w", name, project, ErrNotFound)
	}
	return w, err
}

func (s *Store) ClaimedSessionIDs(harness string) (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT session_id FROM windows WHERE harness = ? AND session_id != ''`, harness)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

func (s *Store) SetSessionID(windowID int64, sessionID string) error {
	_, err := s.db.Exec(`UPDATE windows SET session_id = ? WHERE id = ? AND session_id = ''`, sessionID, windowID)
	return err
}

func (s *Store) CloseWindow(windowID int64) error {
	_, err := s.db.Exec(`UPDATE windows SET closed_at = ? WHERE id = ? AND closed_at IS NULL`, now(), windowID)
	return err
}

func (s *Store) OpenWindowByTarget(target string) (Window, error) {
	w, err := scanWindow(s.db.QueryRow(`SELECT `+windowCols+` FROM windows WHERE tmux_target = ? AND closed_at IS NULL`, target))
	if err == sql.ErrNoRows {
		return w, fmt.Errorf("window %s: %w", target, ErrNotFound)
	}
	return w, err
}
