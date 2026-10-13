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

// Member is a subagent or teammate running inside a session.
type Member struct {
	Name     string
	Teammate bool
	Status   Status
}

// Session is one harness process. ID is "" when the adapter could not tell.
type Session struct {
	Harness    string
	ID         string
	PID        int
	TTY        string
	Cwd        string
	Repo       string
	Branch     string
	Title      string
	Status     Status
	WaitingFor string
	Since      time.Time
	Crew       []Member
	Prompt     *Prompt
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

var ErrNotFound = errors.New("not found")
