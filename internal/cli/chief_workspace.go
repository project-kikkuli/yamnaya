package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func accessArgs(rec launch.Record) []string {
	if rec.Access == "" {
		return nil
	}
	return []string{"--add-dir", rec.Access}
}

func (l launcher) chiefRecord(rec launch.Record) (launch.Record, error) {
	if l.core == nil || l.core.Store == nil {
		return rec, nil
	}
	org, err := l.core.LoadOrg()
	if err != nil {
		return rec, err
	}
	m, ok := org.ManagerOf(rec.Session)
	if !ok || m.Role != core.RoleChief || m.Session != rec.Session {
		return rec, nil
	}
	dir := filepath.Join(l.core.StateDir, "chiefs", m.ID, "workspace")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return rec, err
	}
	if rec.Worktree.Path != dir {
		if rec.Worktree.GitDir != "" {
			m.Repo, rec.Access = "", ""
		}
		if rec.PreviousWorktree.Path == "" {
			rec.PreviousWorktree = rec.Worktree
		}
		rec.Worktree = launch.Worktree{Path: dir}
		rec.Head, rec.Repo = "", ""
	}
	if m.Repo != "" {
		rec.Access = m.Repo
	}
	if err := l.records.Save(rec); err != nil {
		return rec, err
	}
	m.Directory = dir
	return rec, l.core.Store.SaveManager(m)
}

func (l launcher) ChiefWorkspaces(b core.Board) error {
	org, err := l.core.LoadOrg()
	if err != nil {
		return err
	}
	for _, m := range org.Ordered() {
		if m.Role != core.RoleChief || l.core.Off(m.Session) {
			continue
		}
		rec, ok := l.records.Find(m.Session)
		if !ok {
			continue
		}
		dir := filepath.Join(l.core.StateDir, "chiefs", m.ID, "workspace")
		if rec.Worktree.Path == dir && m.Directory == dir {
			continue
		}
		if s, alive := b.Session(m.Session); alive {
			if !s.Settled() {
				continue
			}
			if _, err := l.core.Stop(s); err != nil {
				return fmt.Errorf("workspace for %s: %w", m.ID, err)
			}
		} else if _, state := l.state(rec); state == "running" || state == core.LaunchUnknown {
			continue
		}
		l.resumeNote = "ym: your coordination workspace is now " + dir + ". Your conversation, reports, brief and login are unchanged. This workspace is not a repository. Use the original item and ym item escalate for requests from reports; do not refile them under your own identity."
		if _, err := l.revive(rec); err != nil {
			return fmt.Errorf("workspace for %s: %w", m.ID, err)
		}
	}
	return nil
}
