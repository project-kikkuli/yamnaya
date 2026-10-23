package cli

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

// trustPrompt is the line Claude Code's folder trust dialog offers to accept.
const trustPrompt = "Yes, I trust this folder"

// unstallWithin is how long after its start a launched Claude session is checked for a trust dialog.
const unstallWithin = 10 * time.Minute

// unstall restarts each recently launched Claude session that sits at the folder trust dialog after trusting its worktree again:
// Claude Code rewrites its config without ym's lock, so another session's save can drop the trust ym recorded before the launch.
func (l launcher) unstall(now time.Time) error {
	all, err := l.records.All()
	if err != nil {
		return err
	}
	panes, err := l.srv.Panes()
	if err != nil {
		return err
	}
	var errs []error
	for _, rec := range all {
		if rec.Harness != claude.Name || rec.IsLanded() || now.Sub(rec.Started) > unstallWithin {
			continue
		}
		name := tmux.SessionName(rec.Key)
		if p, ok := panes[name]; !ok || p.Dead {
			continue
		}
		screen, err := l.srv.Capture(name)
		if err != nil || !strings.Contains(screen, trustPrompt) {
			continue
		}
		argv, err := startArgv(rec)
		if err == nil {
			err = launch.Trust(rec.Harness, rec.Worktree.Path)
		}
		if err == nil {
			err = l.srv.Respawn(name, rec.Worktree.Path, argv)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s stopped at the trust dialog and did not restart: %w", rec.Key, err))
		}
	}
	return errors.Join(errs...)
}
