package codex

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
)

func (a *Adapter) QuotaDialog(text string) bool {
	input, ok := a.IdleComposer(text)
	return ok && input == ""
}

func (a *Adapter) handoffScan(path, cwd, id string) (scan, error) {
	root, err := filepath.EvalSymlinks(a.Home)
	if err != nil {
		return scan{}, err
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return scan{}, err
	}
	rel, err := filepath.Rel(root, real)
	if err != nil || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !strings.HasPrefix(rel, "sessions"+string(filepath.Separator)) && !strings.HasPrefix(rel, "archived_sessions"+string(filepath.Separator)) {
		return scan{}, fmt.Errorf("native rollout is outside selected profile history")
	}
	f, err := nativefile.Open(path)
	if err != nil {
		return scan{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return scan{}, err
	}
	sc := a.scan(path, cwd)
	if sc.Invalid || sc.SessionID != id || sc.Offset == 0 || sc.Offset != info.Size() || len(sc.Calls) != 0 || !sc.Stopped {
		return sc, fmt.Errorf("native thread is incomplete or has outstanding work")
	}
	return sc, nil
}

func (a *Adapter) InspectHandoff(s core.Session) (time.Time, error) {
	db, ok := a.db()
	if !ok {
		return time.Time{}, fmt.Errorf("native thread database unavailable")
	}
	sc, err := a.handoffScan(a.Transcript(s.ID), s.Cwd, s.ID)
	if err != nil {
		return time.Time{}, err
	}
	if sc.QuotaAt.IsZero() {
		return time.Time{}, fmt.Errorf("main turn has no terminal subscription quota error")
	}
	rows, err := db.Query(`WITH RECURSIVE tree(id) AS (
		SELECT child_thread_id FROM thread_spawn_edges WHERE parent_thread_id = ?
		UNION SELECT e.child_thread_id FROM thread_spawn_edges e JOIN tree ON e.parent_thread_id = tree.id)
		SELECT tree.id, COALESCE(t.rollout_path,''), COALESCE(t.cwd,'') FROM tree LEFT JOIN threads t ON t.id = tree.id`, s.ID)
	if err != nil {
		return time.Time{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, path, cwd string
		if err = rows.Scan(&id, &path, &cwd); err != nil {
			return time.Time{}, err
		}
		if _, err = a.handoffScan(path, cwd, id); err != nil {
			return time.Time{}, fmt.Errorf("child thread: %w", err)
		}
	}
	return sc.QuotaAt, rows.Err()
}
