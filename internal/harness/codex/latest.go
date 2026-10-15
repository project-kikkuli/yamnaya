package codex

import (
	"path/filepath"
	"time"
)

// LatestIn is the newest thread started in cwd and written since since, or "".
func (a *Adapter) LatestIn(cwd string, since time.Time) string {
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	db, ok := a.db()
	if !ok {
		return ""
	}
	defer db.Close()
	for _, t := range threadsIn(db, cwd) {
		if !t.updated.Before(since.Truncate(time.Second)) {
			return t.id
		}
	}
	return ""
}
