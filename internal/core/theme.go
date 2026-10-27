package core

import (
	"database/sql"
	"errors"
)

const themeSchema = `
CREATE TABLE IF NOT EXISTS ym_theme (
	id INTEGER PRIMARY KEY CHECK (id = 1),
	name TEXT NOT NULL
);
`

// Theme is the name of the theme the board draws behind it, "" until the owner picks one.
func (s *Store) Theme() (string, error) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM ym_theme WHERE id = 1`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return name, err
}

// SetTheme records the theme the board draws behind it.
func (s *Store) SetTheme(name string) error {
	_, err := s.db.Exec(`INSERT INTO ym_theme (id, name) VALUES (1, ?) ON CONFLICT (id) DO UPDATE SET name = excluded.name`, name)
	return err
}
