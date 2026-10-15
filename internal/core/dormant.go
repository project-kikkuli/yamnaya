package core

import (
	"fmt"
	"time"
)

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

// Launches lists, revives and adopts the sessions on ym's server, and sends sessions follow-ups.
type Launches interface {
	Dormant() []Dormant
	Revive(key string) (string, error)
	// Adopt waits until s sits idle, ends it in its current terminal and resumes it on ym's server.
	Adopt(s Live, say func(string)) (Target, error)
	// Send types text into s on ym's server, or leaves it for the agent's next ym item inbox.
	Send(s Live, text string) (string, error)
	// Start launches a new agent in its own worktree on ym's server.
	Start(n NewLaunch) (string, error)
	// Land ends a launched session and removes its worktree, only when its branch is clean and merged or pushed.
	Land(ref string) (string, error)
	Launched() []Launched
}

// Adoptable reports whether ym can move s onto its own server: it runs in a terminal app no host of ym's reaches.
func (l Live) Adoptable() bool { return l.Unadoptable() == "" }

// Unadoptable says why ym cannot move s onto its own server, or is empty when it can.
func (l Live) Unadoptable() string {
	switch {
	case l.Handle != nil:
		return fmt.Sprintf("%s runs in %s; ym open %s reaches it there", Short(l.Key()), l.Location(), Short(l.Key()))
	case l.PID == 0 || l.ID == "":
		return fmt.Sprintf("%s has no session id yet", Short(l.Key()))
	case l.Muxed:
		return fmt.Sprintf("%s runs under a tmux server ym does not reach; quit it there and ym revive or resume it", Short(l.Key()))
	}
	return ""
}

// NewLaunch asks for a new agent on Prompt in a fresh worktree of the repository Repo lies in.
type NewLaunch struct {
	Prompt  string
	Harness string
	Repo    string
	// Slug names the worktree and branch; empty derives it from Prompt.
	Slug string
	// Parent is the session that spawned this one, or empty.
	Parent string
}

// Launched is one session ym launched and how its pane stands: running, exited with a status, or gone.
type Launched struct {
	Key     string
	Harness string
	Session string
	State   string
	Path    string
}

func (l Launched) String() string {
	return fmt.Sprintf("%-6s %-8s %-10s %-28s %s", l.Harness, Short(l.Session), l.State, l.Key, l.Path)
}
