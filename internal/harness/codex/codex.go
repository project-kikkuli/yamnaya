// Package codex reads Codex's thread database and rollout files.
package codex

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	_ "modernc.org/sqlite"
)

const Name = "codex"

const staleAfter = 15 * time.Minute

type Adapter struct {
	Home string
	Bin  string
	// OpenFiles lists the files a process holds open.
	OpenFiles func(pid int) []string
	Cwd       func(pid int) string
	Now       func() time.Time

	mu    sync.Mutex
	scans map[string]*scan
}

func New() *Adapter {
	home := os.Getenv("CODEX_HOME")
	if home == "" {
		h, _ := os.UserHomeDir()
		home = filepath.Join(h, ".codex")
	}
	bin := os.Getenv("YAMNAYA_CODEX_BIN")
	if bin == "" {
		bin = "codex"
	}
	return &Adapter{Home: home, Bin: bin, OpenFiles: lsofFiles, Cwd: lsofCwd, Now: time.Now}
}

func (a *Adapter) Name() string { return Name }

var notSessions = map[string]bool{"app-server": true, "mcp": true, "mcp-server": true, "login": true, "logout": true, "completion": true, "--version": true, "-V": true, "proto": true, "apply": true, "sandbox": true, "debug": true, "features": true}

func (a *Adapter) Owns(p proc.Proc) bool {
	if p.Command() != "codex" {
		return false
	}
	for _, arg := range p.Params() {
		if notSessions[arg] {
			return false
		}
		if !strings.HasPrefix(arg, "-") {
			break
		}
	}
	return true
}

func (a *Adapter) Resume(id string) []string { return []string{a.Bin, "resume", id} }

type thread struct {
	id, rollout, title, cwd string
	updated                 time.Time
}

func argThread(args []string) string {
	for i, arg := range args {
		if arg == "resume" && i+1 < len(args) && len(args[i+1]) >= 32 {
			return args[i+1]
		}
	}
	return ""
}

func (a *Adapter) db() (*sql.DB, bool) {
	matches, _ := filepath.Glob(filepath.Join(a.Home, "state_*.sqlite"))
	if len(matches) == 0 {
		return nil, false
	}
	sort.Strings(matches)
	db, err := sql.Open("sqlite", "file:"+matches[len(matches)-1]+"?mode=ro&"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
	return db, err == nil
}

const threadCols = `id, rollout_path, COALESCE(NULLIF(name, ''), NULLIF(title, ''), first_user_message), cwd, updated_at`

func scanThreads(rows *sql.Rows) []thread {
	defer rows.Close()
	var out []thread
	for rows.Next() {
		var t thread
		var updated int64
		if rows.Scan(&t.id, &t.rollout, &t.title, &t.cwd, &updated) == nil {
			t.updated = time.Unix(updated, 0)
			out = append(out, t)
		}
	}
	return out
}

func (a *Adapter) threadByID(id string) (thread, bool) {
	db, ok := a.db()
	if !ok {
		return thread{}, false
	}
	defer db.Close()
	rows, err := db.Query(`SELECT `+threadCols+` FROM threads WHERE id = ?`, id)
	if err != nil {
		return thread{}, false
	}
	ts := scanThreads(rows)
	if len(ts) == 0 {
		return thread{}, false
	}
	return ts[0], true
}

func (a *Adapter) threadsIn(cwd string) []thread {
	db, ok := a.db()
	if !ok || cwd == "" {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT `+threadCols+` FROM threads WHERE cwd = ? AND archived = 0 ORDER BY updated_at DESC`, cwd)
	if err != nil {
		return nil
	}
	return scanThreads(rows)
}

// heldThread is the thread whose rollout or writer lock the process holds open.
func (a *Adapter) heldThread(pid int) string {
	if a.OpenFiles == nil {
		return ""
	}
	for _, f := range a.OpenFiles(pid) {
		base := filepath.Base(f)
		switch {
		case strings.HasPrefix(base, "rollout-") && strings.HasSuffix(base, ".jsonl"):
			name := strings.TrimSuffix(base, ".jsonl")
			if len(name) >= 36 {
				return name[len(name)-36:]
			}
		case filepath.Base(filepath.Dir(f)) == "thread-writer-locks" && strings.HasSuffix(base, ".lock"):
			return strings.TrimSuffix(base, ".lock")
		}
	}
	return ""
}

func (a *Adapter) SessionID(p proc.Proc) string {
	if id := argThread(p.Args); id != "" {
		return id
	}
	if id := a.heldThread(p.PID); id != "" {
		return id
	}
	t, ok := a.newestSince(p, a.Now(), nil)
	if !ok {
		return ""
	}
	return t.id
}

// newestSince is the newest unclaimed thread in the process's directory written since it started.
func (a *Adapter) newestSince(p proc.Proc, now time.Time, claimed map[string]bool) (thread, bool) {
	cwd := ""
	if a.Cwd != nil {
		cwd = a.Cwd(p.PID)
	}
	started := p.Started(now).Truncate(time.Second)
	for _, t := range a.threadsIn(cwd) {
		if !claimed[t.id] && !t.updated.Before(started) {
			return t, true
		}
	}
	return thread{}, false
}

// Discover resolves every thread named by arguments or open files before guessing the rest from their directories.
func (a *Adapter) Discover(procs []proc.Proc) []core.Session {
	now := a.Now()
	ids := make([]string, len(procs))
	claimed := map[string]bool{}
	for i, p := range procs {
		if ids[i] = argThread(p.Args); ids[i] == "" {
			ids[i] = a.heldThread(p.PID)
		}
		claimed[ids[i]] = ids[i] != ""
	}
	out := make([]core.Session, 0, len(procs))
	for i, p := range procs {
		out = append(out, a.read(p, ids[i], now, claimed))
	}
	return out
}

func (a *Adapter) read(p proc.Proc, id string, now time.Time, claimed map[string]bool) core.Session {
	s := core.Session{Harness: Name, PID: p.PID, TTY: p.TTY}
	var t thread
	var ok bool
	if id != "" {
		if t, ok = a.threadByID(id); !ok {
			t, ok = thread{id: id}, true
		}
	} else if t, ok = a.newestSince(p, now, claimed); ok {
		claimed[t.id] = true
	}
	if !ok {
		if a.Cwd != nil {
			s.Cwd = a.Cwd(p.PID)
		}
		return s
	}
	s.ID, s.Title, s.Cwd = t.id, t.title, t.cwd
	if s.Cwd == "" && a.Cwd != nil {
		s.Cwd = a.Cwd(p.PID)
	}
	if t.rollout == "" {
		return s
	}
	sc := a.scan(t.rollout)
	s.Since = sc.modified
	if sc.open && now.Sub(sc.modified) < staleAfter {
		s.Status = core.Busy
		s.CheckScreen = true
	} else if !sc.modified.IsZero() {
		s.Status = core.Idle
	}
	return s
}

type scan struct {
	offset   int64
	open     bool
	modified time.Time
}

func (a *Adapter) scan(path string) scan {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.scans == nil {
		a.scans = map[string]*scan{}
	}
	t := a.scans[path]
	if t == nil {
		t = &scan{}
		a.scans[path] = t
	}
	f, err := os.Open(path)
	if err != nil {
		return *t
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return *t
	}
	if info.Size() < t.offset {
		*t = scan{}
	}
	t.modified = info.ModTime()
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return *t
	}
	r := bufio.NewReaderSize(f, 1<<16)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		t.offset += int64(len(line))
		var rec struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Type != "event_msg" {
			continue
		}
		switch rec.Payload.Type {
		case "task_started":
			t.open = true
		case "task_complete", "turn_aborted":
			t.open = false
		}
	}
	return *t
}

func lsof(pid int, args ...string) []string {
	out, err := exec.Command("lsof", append([]string{"-a", "-Fn", "-p", strconv.Itoa(pid)}, args...)...).Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			names = append(names, line[1:])
		}
	}
	return names
}

func lsofFiles(pid int) []string { return lsof(pid) }

func lsofCwd(pid int) string {
	if names := lsof(pid, "-d", "cwd"); len(names) > 0 {
		return names[0]
	}
	return ""
}
