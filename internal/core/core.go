// Package core holds sessions, projects, items and phases, and the operations on them.
// Harness and Host adapters plug in through the interfaces below.
package core

import (
	"errors"
	"strconv"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

type Status int

const (
	Unknown Status = iota
	Busy
	Waiting
	Idle
	Done
)

func (s Status) String() string {
	return [...]string{"unknown", "busy", "waiting", "idle", "done"}[s]
}

// Prompt is a harness dialog waiting for the owner, such as a tool permission.
type Prompt struct {
	Tool   string
	Detail string
	From   string
}

func (p Prompt) String() string {
	s := p.Tool
	if p.Detail != "" {
		s += ": " + p.Detail
	}
	if p.From != "" {
		s += " (" + p.From + ")"
	}
	return s
}

// Action is one tool call rendered short, such as "Bash: gh pr create".
type Action struct {
	Tool   string
	Detail string
	At     time.Time
}

func (a Action) String() string {
	if a.Detail == "" {
		return a.Tool
	}
	return a.Tool + ": " + a.Detail
}

// PR is a pull request a session opened, with the last status ym fetched for it.
type PR struct {
	URL    string
	Repo   string
	Number int
	Status *PRStatus
}

// Facts are what a transcript shows about its agent, read without the agent's help.
type Facts struct {
	Task   string
	Ask    string
	Doing  Action
	Result string
	Model  string
	Tools  map[string]int
	Files  []string
	Trail  []Action
	PRs    []PR
}

// Member is a subagent or teammate running inside a session.
type Member struct {
	ID          string
	Name        string
	Type        string
	Description string
	Teammate    bool
	Status      Status
	// Cut reports that the agent ended mid-turn: killed, or failed before it answered.
	Cut  bool
	Last time.Time
	// Brief is the full instruction the agent started from.
	Brief    string
	Worktree string
	// Branch is checked out in Worktree; Ahead counts its commits past the default branch.
	Branch string
	Ahead  int
	// RepoKey is the git common dir of Worktree's repository; Default its default branch.
	RepoKey string
	Default string
	Facts
}

// Session is one harness process. ID is "" when the adapter could not tell.
type Session struct {
	Harness string
	ID      string
	// Parent is the session that spawned this one, when the harness records it.
	Parent string
	PID    int
	TTY    string
	Cwd    string
	// Repo names the repository; RepoKey is its git common dir, shared by all its worktrees.
	Repo     string
	RepoKey  string
	Worktree string
	Branch   string
	// Default is the repository's default branch, "" when unknown.
	Default string
	// Ahead counts Branch's commits past the default branch.
	Ahead      int
	Title      string
	Status     Status
	WaitingFor string
	Since      time.Time
	Crew       []Member
	Prompt     *Prompt
	Facts
	// CheckScreen asks core to look for a prompt on screen because the harness files cannot show one.
	CheckScreen bool
}

// Key identifies the session's conversation, falling back to its process.
func (s Session) Key() string {
	if s.ID != "" {
		return s.ID
	}
	return "pid:" + strconv.Itoa(s.PID)
}

// Harness reads one agent CLI's own files. Every method fails soft: a field it cannot read stays zero.
type Harness interface {
	Name() string
	// Owns reports whether p runs a session of this harness.
	Owns(p proc.Proc) bool
	// Discover reads the sessions run by the given owned processes, all attached to a terminal.
	Discover(procs []proc.Proc) []Session
	// SessionID maps an owned process to its session id.
	SessionID(p proc.Proc) string
	// Resume is the command that reopens a session.
	Resume(id string) []string
	// ParsePrompt finds a live permission dialog on a terminal screen.
	ParsePrompt(screen string) (Prompt, bool)
}

// Handle points at one terminal inside a host.
type Handle struct {
	Host  string
	ID    string
	Label string
}

// Host is a terminal program that can show a session.
type Host interface {
	Name() string
	Locate(tty string) (Handle, bool)
	// Focus brings the terminal forward. When another terminal shows this one, Focus returns that terminal's tty.
	Focus(h Handle) (outer string, err error)
	Type(h Handle, text string) error
	Screen(h Handle) (string, error)
}

// ScanCache keeps what an adapter learned from a file and how far it read, across ym runs.
type ScanCache interface {
	LoadScan(path string) (offset int64, data []byte, ok bool)
	SaveScan(path string, offset int64, data []byte) error
}

var ErrNotFound = errors.New("not found")
