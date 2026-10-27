package cli

import (
	"os"
	"path/filepath"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

// seenRefresh is how often the board rewrites when it last saw a session ym never ran.
const seenRefresh = time.Hour

// seenKeep is how long a session ym never ran stays dormant on the board after it was last seen running; ym revive resumes it after.
const seenKeep = 24 * time.Hour

// Saw records each local session the board shows that ym did not launch, so once its process ends ym revive and the
// dormant list still reach its conversation.
func (l launcher) Saw(sessions []core.Live) {
	var bySession map[string]launch.Record
	now := time.Now()
	for _, s := range sessions {
		if s.Remote != "" || s.Background || s.ID == "" || s.Cwd == "" || launch.Unadoptable(s.Cwd, filepath.Dir(l.records.Dir), !l.everywhere) != "" || s.Status == core.Moved || (s.Handle != nil && s.Handle.Host == l.srv.Name()) || (s.Harness != claude.Name && s.Harness != codex.Name) {
			continue
		}
		if bySession == nil {
			all, _ := l.records.All()
			bySession = map[string]launch.Record{}
			for _, r := range all {
				if prev, ok := bySession[r.Session]; !ok || prev.IsLanded() {
					bySession[r.Session] = r
				}
			}
		}
		rec, ok := bySession[s.ID]
		switch {
		case !ok:
			rec = launch.Adopted(s, now)
			rec.Seen = now
		case rec.Seen.IsZero():
			continue
		case rec.IsLanded():
			rec.Landed, rec.Killed, rec.Started, rec.Seen = time.Time{}, time.Time{}, now, now
		case now.Sub(rec.Seen) >= seenRefresh:
			rec.Seen = now
		default:
			continue
		}
		l.records.Save(rec)
	}
}

// codeGone says why a revive of rec brings back only its conversation: its worktree and its repository are both gone.
func codeGone(rec launch.Record) string {
	if _, err := os.Stat(rec.Worktree.Path); err == nil {
		return ""
	}
	if rec.Worktree.GitDir == "" {
		return launch.DirGone
	}
	if _, err := os.Stat(rec.Worktree.GitDir); err == nil {
		return ""
	}
	return launch.RepoGone
}

// clones are the git dirs of other clones of rec's repository on this host, for a revive whose own clone is gone.
func (l launcher) clones(rec launch.Record) []string {
	if rec.Repo == "" {
		return nil
	}
	if _, err := os.Stat(rec.Worktree.GitDir); err == nil {
		return nil
	}
	var mains []string
	if l.core != nil {
		mains, _ = l.core.KnownRepos()
	}
	found := make(chan string)
	go core.ScanRepos(core.RepoRoots(), found)
	for m := range found {
		mains = append(mains, m)
	}
	seen := map[string]bool{}
	var out []string
	for _, m := range mains {
		gd := filepath.Join(m, ".git")
		if seen[gd] || gd == rec.Worktree.GitDir {
			continue
		}
		seen[gd] = true
		if fi, err := os.Stat(gd); err == nil && fi.IsDir() && core.RepoID(gd) == rec.Repo {
			out = append(out, gd)
		}
	}
	return out
}
