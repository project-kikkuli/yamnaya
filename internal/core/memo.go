package core

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"sync"
)

// memo keeps what the board reads from the database while the database is unchanged, as SQLite's data_version tells on a
// connection that never writes: any commit by any other connection, in this process or another, changes it.
type memo struct {
	mu      sync.Mutex
	conn    *sql.Conn
	version int64
	vals    map[string]any
}

func (m *memo) close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.conn != nil {
		m.conn.Close()
		m.conn = nil
	}
}

// current is the database's data_version, false when it cannot be read and nothing may be kept.
func (m *memo) current(db *sql.DB) (int64, bool) {
	if m.conn == nil {
		c, err := db.Conn(context.Background())
		if err != nil {
			return 0, false
		}
		m.conn = c
	}
	var v int64
	if err := m.conn.QueryRowContext(context.Background(), `PRAGMA data_version`).Scan(&v); err != nil {
		m.conn.Close()
		m.conn = nil
		return 0, false
	}
	return v, true
}

// remembered is load's result for key, read again only after the database changes; clone keeps callers from sharing it.
func remembered[T any](s *Store, key string, clone func(T) T, load func() (T, error)) (T, error) {
	m := &s.memo
	m.mu.Lock()
	v, ok := m.current(s.db)
	if ok && v == m.version && m.vals != nil {
		if got, hit := m.vals[key]; hit {
			m.mu.Unlock()
			return clone(got.(T)), nil
		}
	}
	m.mu.Unlock()
	got, err := load()
	if err != nil || !ok {
		return got, err
	}
	m.mu.Lock()
	if v != m.version || m.vals == nil {
		m.version, m.vals = v, map[string]any{}
	}
	m.vals[key] = got
	m.mu.Unlock()
	return clone(got), nil
}

func cloneSlice[T any](s []T) []T { return slices.Clone(s) }

func cloneMap[K comparable, V any](m map[K]V) map[K]V { return maps.Clone(m) }

func asIs[T any](v T) T { return v }
