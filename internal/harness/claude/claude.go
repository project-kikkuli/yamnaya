// Package claude reads Claude Code's per-process state, transcripts and subagent files.
package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"os/exec"
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

	mu    sync.Mutex
	scans map[string]*scan
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
	return &Adapter{Dir: dir, Bin: bin, Cwd: cwdOf, Now: time.Now}
}

func (a *Adapter) Name() string { return Name }

var subcommands = map[string]bool{"mcp": true, "doctor": true, "update": true, "install": true, "config": true, "setup-token": true, "plugin": true}

func (a *Adapter) Owns(p proc.Proc) bool {
	if p.Command() != "claude" {
		return false
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
		s.ID, s.Cwd = st.SessionID, st.Cwd
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
	s.Facts = sc.Facts
	s.Task = sc.First
	if s.Ask == "" {
		s.Ask = sc.LastPrompt
	}
	if path != "" {
		s.Crew = a.crew(filepath.Join(strings.TrimSuffix(path, ".jsonl"), "subagents"), s.Status == core.Busy, now)
	}
	return s
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// cwdOf reads a process's working directory with lsof.
func cwdOf(pid int) string {
	out, err := exec.Command("lsof", "-a", "-d", "cwd", "-Fn", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			return line[1:]
		}
	}
	return ""
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
