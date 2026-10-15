package core

import "time"

// Dormant is a session ym launched whose agent no longer runs; reviving it resumes it in its worktree.
type Dormant struct {
	Key     string
	Harness string
	Session string
	Parent  string
	Cwd     string
	// RepoKey is the git common dir of the repository its worktree belongs to.
	RepoKey string
	Branch  string
	Title   string
	// State is how its pane stands, such as "exited 0" or "gone".
	State   string
	Started time.Time
}

// ID is the session's key on the board: its session id, else its launch key.
func (d Dormant) ID() string {
	if d.Session != "" {
		return d.Session
	}
	return "launch:" + d.Key
}

// Launches lists and revives the sessions ym launched.
type Launches interface {
	Dormant() []Dormant
	Revive(key string) (string, error)
}
