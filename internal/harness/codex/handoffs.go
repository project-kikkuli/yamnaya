package codex

import (
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/handoff"
)

func (a *Adapter) HandoffFiles(session, cwd string) ([]handoff.File, error) {
	db, ok := a.db()
	if !ok {
		return nil, fmt.Errorf("codex thread database is unavailable")
	}
	defer db.Close()
	t, ok := threadByID(db, session)
	if !ok || t.rollout == "" {
		return nil, fmt.Errorf("codex rollout for %s is unavailable", session)
	}
	out := []handoff.File{{Harness: Name, Session: session, Agent: session, Path: t.rollout}}
	for _, child := range spawned(db, session) {
		if child.rollout != "" {
			out = append(out, handoff.File{Harness: Name, Session: session, Agent: child.id, Path: child.rollout})
		}
	}
	return out, nil
}
