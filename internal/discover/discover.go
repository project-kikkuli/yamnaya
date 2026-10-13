// Package discover finds the Claude Code and Codex sessions running in tmux panes and reads their title, status, and crew.
package discover

import (
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
)

type Status int

const (
	Running Status = iota
	NeedsYou
	YourTurn
	Idle
)

// YourTurnFor is how long a finished turn reads as waiting on you before it fades to idle.
const YourTurnFor = 30 * time.Minute

type Session struct {
	Harness     string
	ID          string
	PID         int
	Title       string
	Cwd         string
	Repo        string
	Branch      string
	Pane        string
	WindowID    string
	TmuxSession string
	WindowIndex int
	WindowName  string
	Status      Status
	WaitingFor  string
	Since       time.Time
	Crew        []Member
}

func (s Session) CrewRunning() int {
	n := 0
	for _, m := range s.Crew {
		if m.Status == CrewRunning {
			n++
		}
	}
	return n
}

type Discoverer struct {
	ClaudeDir string
	CodexHome string
	Panes     func() ([]tmux.PaneInfo, error)
	Procs     func() ([]Proc, error)
	Cwds      func(pids []int) map[int]string
	Git       func(dir string) (repo, branch string)
	Now       func() time.Time

	mu     sync.Mutex
	titles map[string]*titleScan
	git    map[string]gitEntry
}

func New(t tmux.Client) *Discoverer {
	home, _ := os.UserHomeDir()
	d := &Discoverer{ClaudeDir: os.Getenv("CLAUDE_CONFIG_DIR"), CodexHome: os.Getenv("CODEX_HOME")}
	if d.ClaudeDir == "" {
		d.ClaudeDir = filepath.Join(home, ".claude")
	}
	if d.CodexHome == "" {
		d.CodexHome = filepath.Join(home, ".codex")
	}
	d.Panes, d.Procs, d.Cwds, d.Git, d.Now = t.Panes, PS, LsofCwds, gitInfo, time.Now
	return d
}

// Discover lists every live harness process under a tmux pane, ordered by tmux position.
func (d *Discoverer) Discover() ([]Session, error) {
	panes, err := d.Panes()
	if err != nil {
		return nil, err
	}
	procs, err := d.Procs()
	if err != nil {
		return nil, err
	}
	now := d.Now()
	found := harnessProcs(panes, procs)
	var missing []int
	for _, f := range found {
		if f.harness == harness.Codex || !d.hasClaudeState(f.proc.PID) {
			missing = append(missing, f.proc.PID)
		}
	}
	var cwds map[int]string
	if len(missing) > 0 {
		cwds = d.Cwds(missing)
	}
	var sessions []Session
	claimed := map[string]bool{}
	for _, f := range found {
		s := Session{
			Harness: f.harness, PID: f.proc.PID, Pane: f.pane.ID, WindowID: f.pane.WindowID,
			TmuxSession: f.pane.Session, WindowIndex: f.pane.WindowIndex, WindowName: f.pane.WindowName,
			Cwd: cwds[f.proc.PID],
		}
		if s.Cwd == "" {
			s.Cwd = f.pane.Cwd
		}
		started := now.Add(-f.proc.Elapsed)
		switch f.harness {
		case harness.Claude:
			d.readClaude(&s, f.proc, started, now)
		case harness.Codex:
			d.readCodex(&s, f.proc, started, now, claimed)
		}
		s.Repo, s.Branch = d.gitFor(s.Cwd, now)
		sessions = append(sessions, s)
	}
	sort.SliceStable(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		if a.TmuxSession != b.TmuxSession {
			return a.TmuxSession < b.TmuxSession
		}
		if a.WindowIndex != b.WindowIndex {
			return a.WindowIndex < b.WindowIndex
		}
		return a.Pane < b.Pane
	})
	return sessions, nil
}

// settle maps a finished turn to waiting on you, fading to idle once it has sat for YourTurnFor.
func settle(since, now time.Time) Status {
	if !since.IsZero() && now.Sub(since) >= YourTurnFor {
		return Idle
	}
	return YourTurn
}
