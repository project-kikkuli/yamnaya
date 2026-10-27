package core

import (
	"fmt"
	"time"
)

// Dormant is a session ym launched whose agent no longer runs; reviving it resumes it in its worktree.
type Dormant struct {
	Profile      string
	ProfileLabel string
	Account      string
	ConfigRoot   string
	Key          string
	Harness      string
	Session      string
	Parent       string
	Cwd          string
	// RepoKey is the git common dir of the repository its worktree belongs to.
	RepoKey string
	// RepoID is the repository's identity across clones and hosts, derived from RepoKey when empty.
	RepoID string
	Branch string
	Title  string
	// State is how its pane stands, such as "exited 1" or "gone", or why landing it after a clean exit failed.
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
	Start(n NewLaunch) (Started, error)
	// End closes a launched session's pane and removes its worktree, first bundling into ym's state any work no remote holds so
	// ym revive restores it; goal tags the session so reviving that goal or chief revives it. It fails with ErrNotFound when ym
	// launched no session ref names.
	End(ref, goal string) (string, error)
	// Weigh is the Toll End would take on the launched session ref names, Sessions left zero.
	Weigh(ref string) (Toll, error)
	// GC removes idle worktrees of known repositories that lose nothing, then branches merged into origin's default branch.
	GC() (string, error)
	// Relaunch resumes session id of harness on ym's server in from's directory, taking over from's launch record when it has one.
	Relaunch(from Live, harness, id string) (string, error)
	Launched() []Launched
}

// Adoptable reports whether ym can move s onto its own server: it runs in a terminal app no host of ym's reaches.
func (l Live) Adoptable() bool { return l.Unadoptable() == "" }

// Unadoptable says why ym cannot move s onto its own server, or is empty when it can.
func (l Live) Unadoptable() string {
	switch {
	case l.Remote != "":
		return fmt.Sprintf("%s runs on %s; ym open %s reaches it there over ssh", Short(l.Key()), l.Remote, Short(l.Key()))
	case l.Background:
		return fmt.Sprintf("%s is a background job with no terminal to move; ym answers and sends reach it through its inbox", Short(l.Key()))
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
	Profile    string
	ConfigRoot string
	Prompt     string
	Harness    string
	Repo       string
	// Slug names the worktree and branch; empty derives it from Prompt.
	Slug string
	// Parent is the session that spawned this one, or empty.
	Parent string
	// Session is the id a Claude session starts with; empty picks a fresh one. Codex picks its own.
	Session string
	// Await has Start wait until a Codex session has recorded its id, so Started carries it.
	Await bool
}

// Started is a session Start launched: its launch key, its session id once known, and the lines that tell the owner where it runs.
type Started struct {
	Key, Session, Message string
	Profile, ConfigRoot   string
}

// Launched is one session ym launched and how its pane stands: running, exited with a status, or gone.
// LaunchUnknown is the state of a launch whose pane tmux could not report: it may still run, so nothing is decided on it.
const LaunchUnknown = "unknown"

// MayRun reports whether the launch's agent runs, or might: tmux could not say.
func (l Launched) MayRun() bool { return l.State == "running" || l.State == LaunchUnknown }

type Launched struct {
	Profile      string
	ProfileLabel string
	Account      string
	Key          string
	Harness      string
	Session      string
	State        string
	Path         string
	// Parent is the session that spawned it, or empty.
	Parent string
	// Landed is when ym landed it, or zero while it is not landed; Killed is when ym kill stopped it, or zero.
	Landed, Killed time.Time
	// Revive is set when it was running as ym's tmux server went away, so ym resumes it.
	Revive bool
}

func (l Launched) String() string {
	return fmt.Sprintf("%-6s %-8s %-10s %-28s %s", l.Harness, Short(l.Session), l.State, l.Key, l.Path)
}
