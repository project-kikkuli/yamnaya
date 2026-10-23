// Package claude reads Claude Code's per-process state, transcripts and subagent files.
package claude

import (
	"bufio"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
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
	idle  map[int]idleRead
	found map[string]found

	files    sync.Mutex
	listings map[string]listing
	jsons    map[string]jsonFile
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
	st, _, ok := a.stateAt(pid)
	return st, ok
}

// stateAt is the state file and when Claude Code last wrote it.
func (a *Adapter) stateAt(pid int) (state, time.Time, bool) {
	var st state
	at, ok := a.parsed(filepath.Join(a.Dir, "sessions", strconv.Itoa(pid)+".json"), &st)
	if !ok || st.PID != pid || st.SessionID == "" {
		return state{}, time.Time{}, false
	}
	return st, at, true
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

func (a *Adapter) Resume(id string) []string {
	return []string{a.Bin, "--dangerously-skip-permissions", "--resume", id}
}

func (a *Adapter) Discover(procs []proc.Proc) []core.Session {
	now := a.Now()
	out := make([]core.Session, 0, len(procs))
	running := map[int]bool{}
	for _, p := range procs {
		out = append(out, a.read(p, now))
		running[p.PID] = true
	}
	a.mu.Lock()
	for pid := range a.idle {
		if !running[pid] {
			delete(a.idle, pid)
		}
	}
	a.mu.Unlock()
	return out
}

var nonPathChars = regexp.MustCompile(`[^A-Za-z0-9]`)

func encodeCwd(cwd string) string { return nonPathChars.ReplaceAllString(cwd, "-") }

func (a *Adapter) transcript(cwd, id string) string {
	if cwd != "" {
		p := filepath.Join(a.Dir, "projects", encodeCwd(cwd), id+".jsonl")
		if _, err := os.Stat(p); err == nil {
			a.remember(id, found{path: p})
			return p
		}
	}
	now := time.Now()
	a.mu.Lock()
	f, ok := a.found[id]
	a.mu.Unlock()
	if ok && f.path != "" {
		if _, err := os.Stat(f.path); err == nil {
			return f.path
		}
	} else if ok && now.Before(f.retry) {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(a.Dir, "projects", "*", id+".jsonl"))
	if len(matches) > 0 {
		a.remember(id, found{path: matches[0]})
		return matches[0]
	}
	wait := min(max(2*f.wait, missRetry), maxMissRetry)
	a.remember(id, found{retry: now.Add(wait), wait: wait})
	return ""
}

func (a *Adapter) remember(id string, f found) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.found == nil {
		a.found = map[string]found{}
	}
	a.found[id] = f
}

// A session id no project directory holds is looked for again after missRetry, then twice as long after each miss up to
// maxMissRetry: each look lists every project directory.
const (
	missRetry    = 30 * time.Second
	maxMissRetry = 10 * time.Minute
)

// found is where a session's transcript is, or when to look for a missing one again.
type found struct {
	path  string
	retry time.Time
	wait  time.Duration
}

// An idle session finishes its transcript within a second of Claude Code writing its state file idle, then writes nothing the
// board shows until the state file changes again. One read idleSettle after that write stands for idleRecheck.
const (
	idleSettle  = 5 * time.Second
	idleRecheck = 10 * time.Second
)

type idleRead struct {
	state   state
	tty     string
	at      time.Time
	session core.Session
}

func (a *Adapter) read(p proc.Proc, now time.Time) core.Session {
	st, wrote, ok := a.stateAt(p.PID)
	a.mu.Lock()
	last, seen := a.idle[p.PID]
	a.mu.Unlock()
	if ok && st.Status == "idle" && seen && last.state == st && last.tty == p.TTY && last.at.Sub(wrote) >= idleSettle && now.Sub(last.at) >= 0 && now.Sub(last.at) < idleRecheck {
		s := last.session
		s.Crew = slices.Clone(s.Crew)
		s.Tools = maps.Clone(s.Tools)
		return s
	}
	s := a.readFresh(p, st, ok, now)
	a.mu.Lock()
	if a.idle == nil {
		a.idle = map[int]idleRead{}
	}
	if ok && st.Status == "idle" {
		kept := s
		kept.Crew = slices.Clone(s.Crew)
		kept.Tools = maps.Clone(s.Tools)
		a.idle[p.PID] = idleRead{state: st, tty: p.TTY, at: now, session: kept}
	} else {
		delete(a.idle, p.PID)
	}
	a.mu.Unlock()
	return s
}

func (a *Adapter) readFresh(p proc.Proc, st state, ok bool, now time.Time) core.Session {
	s := core.Session{Harness: Name, PID: p.PID, TTY: p.TTY}
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
