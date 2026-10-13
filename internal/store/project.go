package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	ProjectActive = "active"
	ProjectLanded = "landed"
)

type Project struct {
	Name       string
	Cwd        string
	State      string
	Phases     []string
	PhaseIndex int
	CreatedAt  time.Time
	LandedAt   time.Time
}

const projectCols = `name, cwd, state, phases, phase_index, created_at, landed_at`

func scanProject(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var phases string
	var created, landed sql.NullString
	if err := row.Scan(&p.Name, &p.Cwd, &p.State, &phases, &p.PhaseIndex, &created, &landed); err != nil {
		return p, err
	}
	if err := json.Unmarshal([]byte(phases), &p.Phases); err != nil {
		return p, err
	}
	p.CreatedAt = parseTime(created)
	p.LandedAt = parseTime(landed)
	return p, nil
}

func (s *Store) Project(name string) (Project, error) {
	p, err := scanProject(s.db.QueryRow(`SELECT `+projectCols+` FROM projects WHERE name = ?`, name))
	if errors.Is(err, sql.ErrNoRows) {
		return p, fmt.Errorf("project %q: %w", name, ErrNotFound)
	}
	return p, err
}

func (s *Store) ActiveProject(name string) (Project, error) {
	p, err := s.Project(name)
	if err != nil {
		return p, err
	}
	if p.State != ProjectActive {
		return p, fmt.Errorf("project %q is landed", name)
	}
	return p, nil
}

func (s *Store) Projects(state string) ([]Project, error) {
	rows, err := s.db.Query(`SELECT `+projectCols+` FROM projects WHERE state = ? ORDER BY created_at, name`, state)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Project
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// CreateProject inserts the project and its manager window, calling launch before commit so a failed launch leaves no rows.
func (s *Store) CreateProject(p Project, manager Window, launch func(Window) (string, error)) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	phases, err := json.Marshal(nonNil(p.Phases))
	if err != nil {
		return err
	}
	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM projects WHERE name = ?`, p.Name).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return fmt.Errorf("project %q already exists", p.Name)
	}
	if _, err := tx.Exec(`INSERT INTO projects (name, cwd, state, phases, phase_index, created_at) VALUES (?, ?, ?, ?, 0, ?)`,
		p.Name, p.Cwd, ProjectActive, string(phases), now()); err != nil {
		return err
	}
	if err := insertWindow(tx, manager, launch); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetPhases(name string, phases []string) (Project, error) {
	data, err := json.Marshal(nonNil(phases))
	if err != nil {
		return Project{}, err
	}
	res, err := s.db.Exec(`UPDATE projects SET phases = ?, phase_index = max(0, min(phase_index, ? - 1)) WHERE name = ? AND state = ?`,
		string(data), len(phases), name, ProjectActive)
	if err := requireOne(res, err, name); err != nil {
		return Project{}, err
	}
	return s.Project(name)
}

func (s *Store) NextPhase(name string) (Project, error) {
	p, err := s.ActiveProject(name)
	if err != nil {
		return p, err
	}
	if p.PhaseIndex+1 >= len(p.Phases) {
		return p, fmt.Errorf("project %q is already at its last phase", name)
	}
	res, err := s.db.Exec(`UPDATE projects SET phase_index = phase_index + 1 WHERE name = ? AND phase_index = ?`, name, p.PhaseIndex)
	if err := requireOne(res, err, name); err != nil {
		return p, err
	}
	return s.Project(name)
}

func (s *Store) Land(name string, kill func() error) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ts := now()
	var state string
	if err := tx.QueryRow(`SELECT state FROM projects WHERE name = ?`, name).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("project %q: %w", name, ErrNotFound)
		}
		return err
	}
	if state != ProjectActive {
		return fmt.Errorf("project %q is already landed", name)
	}
	if _, err := tx.Exec(`UPDATE projects SET state = ?, landed_at = ? WHERE name = ?`, ProjectLanded, ts, name); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE windows SET closed_at = ? WHERE project = ? AND closed_at IS NULL`, ts, name); err != nil {
		return err
	}
	if err := kill(); err != nil {
		return err
	}
	return tx.Commit()
}

func requireOne(res sql.Result, err error, name string) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("project %q: %w", name, ErrNotFound)
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
