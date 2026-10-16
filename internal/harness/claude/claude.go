// Package claude reads Claude Code's per-process state, transcripts and subagent files.
package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const Name = "claude"

// staleAfter is how long a transcript mid-turn may stay silent before it no longer reads as busy.
const staleAfter = 15 * time.Minute

type Adapter struct {
	Dir   string
	Bin   string
	Cwd   func(pid int) string
	Now   func() time.Time
	Cache core.ScanCache
	// SendTimeout bounds one delivery to a session's messaging socket; zero means two seconds.
	SendTimeout time.Duration
	// ReceiptWait is how long a send waits for a session to report it held the message; zero means one second.
	ReceiptWait time.Duration
	// ManagedSettings is the organization's settings file, which outranks the owner's own.
	ManagedSettings string

	mu    sync.Mutex
	scans map[string]*scan
	dirs  map[string]string
}

func New() *Adapter {
	dir := os.Getenv("CLAUDE_CONFIG_DIR")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".claude")
	}
	bin := os.Getenv("YAMNAYA_CLAUDE_BIN")
	if bin == "" {
		bin = "claude"
	}
	return &Adapter{Dir: dir, Bin: bin, Cwd: proc.Cwd, Now: time.Now, ManagedSettings: "/Library/Application Support/ClaudeCode/managed-settings.json"}
}

func (a *Adapter) Name() string { return Name }

var subcommands = map[string]bool{"mcp": true, "doctor": true, "update": true, "install": true, "config": true, "setup-token": true, "plugin": true}

// titledRoles are the process titles "claude <role>" that can run a session; a background job's process renames itself "claude bg-spare"
// and runs one once a job claims it and it writes its state file.
var titledRoles = map[string]bool{"bg-spare": true}

func (a *Adapter) Owns(p proc.Proc) bool {
	name, role, titled := strings.Cut(p.Command(), " ")
	if name != "claude" || titled && !titledRoles[role] {
		return false
	}
	if titled {
		_, ok := a.state(p.PID)
		return ok
	}
	args := p.Params()
	if len(args) > 0 && subcommands[args[0]] {
		return false
	}
	for _, arg := range args {
		if arg == "--chrome-native-host" || arg == "--version" {
			return false
		}
	}
	return true
}

// state is the file Claude Code keeps at sessions/<pid>.json while it runs.
type state struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	Cwd             string `json:"cwd"`
	Name            string `json:"name"`
	NameSource      string `json:"nameSource"`
	Status          string `json:"status"`
	WaitingFor      string `json:"waitingFor"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
	PeerProtocol    int    `json:"peerProtocol"`
	MessagingSocket string `json:"messagingSocketPath"`
	Kind            string `json:"kind"`
}

func (a *Adapter) state(pid int) (state, bool) {
	b, err := os.ReadFile(filepath.Join(a.Dir, "sessions", strconv.Itoa(pid)+".json"))
	if err != nil {
		return state{}, false
	}
	var st state
	if json.Unmarshal(b, &st) != nil || st.PID != pid || st.SessionID == "" {
		return state{}, false
	}
	return st, true
}

// liveCwd is the cwd a running process gives for session id, "" when none runs it.
func (a *Adapter) liveCwd(id string) string {
	files, _ := filepath.Glob(filepath.Join(a.Dir, "sessions", "*.json"))
	for _, f := range files {
		pid, err := strconv.Atoi(strings.TrimSuffix(filepath.Base(f), ".json"))
		if err != nil {
			continue
		}
		if st, ok := a.state(pid); ok && st.SessionID == id {
			return st.Cwd
		}
	}
	return ""
}

// Waiting reports whether Claude Code at pid says it waits on a permission or other dialog.
func (a *Adapter) Waiting(pid int) bool {
	st, ok := a.state(pid)
	return ok && st.Status == "waiting"
}

func argSessionID(args []string) string {
	for i, arg := range args {
		for _, flag := range []string{"--resume", "-r", "--session-id"} {
			if arg == flag && i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				return args[i+1]
			}
			if strings.HasPrefix(arg, flag+"=") {
				return strings.TrimPrefix(arg, flag+"=")
			}
		}
	}
	return ""
}

func (a *Adapter) SessionID(p proc.Proc) string {
	if st, ok := a.state(p.PID); ok {
		return st.SessionID
	}
	return argSessionID(p.Args)
}

func (a *Adapter) Resume(id string) []string { return []string{a.Bin, "--resume", id} }

func (a *Adapter) Discover(procs []proc.Proc) []core.Session {
	now := a.Now()
	out := make([]core.Session, 0, len(procs))
	for _, p := range procs {
		out = append(out, a.read(p, now))
	}
	return out
}

var nonPathChars = regexp.MustCompile(`[^A-Za-z0-9]`)

func encodeCwd(cwd string) string { return nonPathChars.ReplaceAllString(cwd, "-") }

func (a *Adapter) transcript(cwd, id string) string {
	if cwd != "" {
		p := filepath.Join(a.Dir, "projects", encodeCwd(cwd), id+".jsonl")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	matches, _ := filepath.Glob(filepath.Join(a.Dir, "projects", "*", id+".jsonl"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

func (a *Adapter) read(p proc.Proc, now time.Time) core.Session {
	s := core.Session{Harness: Name, PID: p.PID, TTY: p.TTY}
	st, ok := a.state(p.PID)
	if ok {
		s.ID, s.Cwd, s.Background = st.SessionID, st.Cwd, st.Kind == "bg"
	} else {
		s.ID = argSessionID(p.Args)
	}
	if s.Cwd == "" && a.Cwd != nil {
		s.Cwd = a.Cwd(p.PID)
	}
	var path string
	if s.ID != "" {
		path = a.transcript(s.Cwd, s.ID)
	}
	sc := a.scan(path)
	s.Title = sc.title()
	if s.Title == "" && ok && st.NameSource != "derived" {
		s.Title = st.Name
	}
	switch {
	case ok && st.Status == "busy":
		s.Status, s.Since = core.Busy, msTime(st.StatusUpdatedAt)
	case ok && st.Status == "waiting":
		s.Status, s.Since, s.WaitingFor = core.Waiting, msTime(st.StatusUpdatedAt), st.WaitingFor
	case ok && st.Status == "idle":
		s.Status, s.Since = core.Idle, msTime(st.StatusUpdatedAt)
	case path != "" && sc.Open && now.Sub(sc.modified) < staleAfter:
		s.Status, s.Since = core.Busy, sc.modified
	case path != "":
		s.Status, s.Since = core.Idle, sc.modified
	}
	if sc.Next != "" {
		s.Status, s.Successor, s.Since = core.Moved, sc.Next, sc.modified
	}
	delegated := s.Status == core.Busy
	if delegated && sc.ended(s.Since) {
		s.Status, s.Since = core.Idle, sc.LastAt
	}
	s.Facts = sc.Facts
	s.Task = sc.First
	if s.Ask == "" {
		s.Ask = sc.LastPrompt
	}
	if path != "" {
		s.Crew = a.crew(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents"), delegated, now)
	}
	return s
}

// Successor is the session id's conversation continued in, as its transcript's last continued-in record says.
// With near set, a running session, it reads only a transcript in near's project directory, so a miss costs one stat.
func (a *Adapter) Successor(id, near string) string {
	path := ""
	if near == "" {
		path = a.transcript("", id)
	} else if dir := a.projectDir(near); dir != "" {
		path = filepath.Join(dir, id+".jsonl")
		if _, err := os.Stat(path); err != nil {
			return ""
		}
	}
	if path == "" {
		return ""
	}
	return a.scan(path).Next
}

// projectDir is the directory holding the transcript of running session id, "" when no process runs it; looked up once per adapter.
func (a *Adapter) projectDir(id string) string {
	a.mu.Lock()
	dir, ok := a.dirs[id]
	a.mu.Unlock()
	if ok {
		return dir
	}
	if cwd := a.liveCwd(id); cwd != "" {
		if path := a.transcript(cwd, id); path != "" {
			dir = filepath.Dir(path)
		}
	}
	a.mu.Lock()
	if a.dirs == nil {
		a.dirs = map[string]string{}
	}
	a.dirs[id] = dir
	a.mu.Unlock()
	return dir
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// SessionCwd is where session worked, from the first transcript record that names a directory.
func (a *Adapter) SessionCwd(session string) string {
	path := a.transcript("", session)
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<16)
	for range 200 {
		line, err := r.ReadBytes('\n')
		var rec struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &rec) == nil && rec.Cwd != "" {
			return rec.Cwd
		}
		if err != nil {
			break
		}
	}
	return ""
}
