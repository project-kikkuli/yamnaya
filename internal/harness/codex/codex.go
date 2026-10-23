// Package codex reads Codex's thread database and rollout files.
package codex

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	_ "modernc.org/sqlite"
)

const Name = "codex"

const staleAfter = 15 * time.Minute

type Adapter struct {
	Home      string
	StateHome string
	Bin       string
	// OpenFiles lists the files a process holds open.
	OpenFiles func(pid int) []string
	Cwd       func(pid int) string
	Now       func() time.Time
	Cache     core.ScanCache

	mu    sync.Mutex
	scans map[string]*scan
	// state stays open on the newest state database, dbPath, so each board reads it without reopening it.
	state  *sql.DB
	dbPath string
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
	return &Adapter{Home: home, Bin: bin, OpenFiles: proc.Files, Cwd: proc.Cwd, Now: time.Now}
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

func (a *Adapter) Resume(id string) []string { return []string{a.Bin, "resume", "--yolo", id} }

// Transcript is the path of thread id's rollout, empty when Codex keeps none.
func (a *Adapter) Transcript(id string) string {
	if db, ok := a.db(); ok {
		var path string
		err := db.QueryRow(`SELECT rollout_path FROM threads WHERE id=?`, id).Scan(&path)
		if err == nil {
			if _, err = os.Stat(path); err == nil {
				return path
			}
			return ""
		}
		if err != sql.ErrNoRows {
			return ""
		}
	}
	var paths []string
	for _, dir := range []string{"sessions", "archived_sessions"} {
		filepath.WalkDir(filepath.Join(a.Home, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !nativefile.IsTranscript(path) {
				return nil
			}
			f, err := nativefile.Open(path)
			if err != nil {
				return nil
			}
			line, _ := bufio.NewReader(f).ReadBytes('\n')
			f.Close()
			var doc struct {
				Type    string `json:"type"`
				Payload struct {
					ID string `json:"id"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &doc) == nil && doc.Type == "session_meta" && doc.Payload.ID == id {
				paths = append(paths, path)
			}
			return nil
		})
	}
	sort.Strings(paths)
	if len(paths) > 0 {
		return paths[len(paths)-1]
	}
	return ""
}

type thread struct {
	id, rollout, title, cwd, task, branch, role, nickname, path, model string
	updated                                                            time.Time
}

var threadID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// argThread is the thread id a resume names, wherever it sits among the resume's flags.
func argThread(args []string) string {
	for i, arg := range args {
		if arg != "resume" {
			continue
		}
		for _, a := range args[i+1:] {
			if threadID.MatchString(a) {
				return a
			}
		}
	}
	return ""
}

func (a *Adapter) db() (*sql.DB, bool) {
	matches, _ := filepath.Glob(filepath.Join(a.sqliteHome(), "state_*.sqlite"))
	if len(matches) == 0 {
		return nil, false
	}
	sort.Strings(matches)
	path := matches[len(matches)-1]
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.dbPath == path {
		return a.state, true
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
	if err != nil {
		return nil, false
	}
	if a.state != nil {
		a.state.Close()
	}
	a.dbPath, a.state = path, db
	return db, true
}

const threadCols = `id, rollout_path, COALESCE(NULLIF(name, ''), NULLIF(title, ''), first_user_message), cwd, updated_at,
	first_user_message, COALESCE(git_branch, ''), COALESCE(agent_role, ''), COALESCE(agent_nickname, ''), COALESCE(agent_path, ''), COALESCE(model, '')`

func scanThreads(rows *sql.Rows) []thread {
	defer rows.Close()
	var out []thread
	for rows.Next() {
		var t thread
		var updated int64
		if rows.Scan(&t.id, &t.rollout, &t.title, &t.cwd, &updated, &t.task, &t.branch, &t.role, &t.nickname, &t.path, &t.model) == nil {
			t.updated = time.Unix(updated, 0)
			out = append(out, t)
		}
	}
	return out
}

func threadByID(db *sql.DB, id string) (thread, bool) {
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

func threadsIn(db *sql.DB, cwd string) []thread {
	if cwd == "" {
		return nil
	}
	rows, err := db.Query(`SELECT `+threadCols+` FROM threads WHERE cwd = ? AND archived = 0
		AND id NOT IN (SELECT child_thread_id FROM thread_spawn_edges) ORDER BY updated_at DESC`, cwd)
	if err != nil {
		return nil
	}
	return scanThreads(rows)
}

// child is a thread spawned under a session, with its spawn edge's status.
type child struct {
	thread
	open bool
}

// spawned lists every thread below id in the spawn tree.
func spawned(db *sql.DB, id string) []child {
	rows, err := db.Query(`WITH RECURSIVE tree(id, status) AS (
		SELECT child_thread_id, status FROM thread_spawn_edges WHERE parent_thread_id = ?
		UNION SELECT e.child_thread_id, e.status FROM thread_spawn_edges e JOIN tree ON e.parent_thread_id = tree.id)
		SELECT tree.status, x.* FROM tree JOIN (SELECT `+threadCols+` FROM threads) x ON x.id = tree.id ORDER BY x.updated_at DESC`, id)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []child
	for rows.Next() {
		var c child
		var status string
		var updated int64
		if rows.Scan(&status, &c.id, &c.rollout, &c.title, &c.cwd, &updated, &c.task, &c.branch, &c.role, &c.nickname, &c.path, &c.model) == nil {
			c.updated, c.open = time.Unix(updated, 0), status == "open"
			out = append(out, c)
		}
	}
	return out
}

func parentOf(db *sql.DB, id string) string {
	var p string
	_ = db.QueryRow(`SELECT parent_thread_id FROM thread_spawn_edges WHERE child_thread_id = ?`, id).Scan(&p)
	return p
}

// heldThreads are the threads whose rollouts or writer locks the process holds open; a thread with subagents holds theirs too.
func (a *Adapter) heldThreads(pid int) []string {
	if a.OpenFiles == nil {
		return nil
	}
	var out []string
	for _, f := range a.OpenFiles(pid) {
		base := filepath.Base(f)
		id := ""
		switch {
		case strings.HasPrefix(base, "rollout-") && nativefile.IsTranscript(base):
			if name := strings.TrimSuffix(nativefile.PlainName(base), ".jsonl"); len(name) >= 36 {
				if before, _, ok := strings.Cut(name, "_"); ok {
					name = before
				}
				id = name[len(name)-36:]
			}
		case filepath.Base(filepath.Dir(f)) == "thread-writer-locks" && strings.HasSuffix(base, ".lock"):
			id = strings.TrimSuffix(base, ".lock")
		}
		if id != "" && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// heldThread is the root among the held threads: the one no other held thread spawned.
func (a *Adapter) heldThread(db *sql.DB, pid int) string {
	ids := a.heldThreads(pid)
	if len(ids) == 0 {
		return ""
	}
	if db != nil {
		for _, id := range ids {
			if p := parentOf(db, id); p == "" || !slices.Contains(ids, p) {
				return id
			}
		}
	}
	return ids[0]
}

func (a *Adapter) SessionID(p proc.Proc) string {
	if id := argThread(p.Args); id != "" {
		return id
	}
	db, ok := a.db()
	if !ok {
		return a.heldThread(nil, p.PID)
	}
	if id := a.heldThread(db, p.PID); id != "" {
		return id
	}
	t, ok := a.newestSince(db, p, a.Now(), nil)
	if !ok {
		return ""
	}
	return t.id
}

// newestSince is the newest unclaimed thread in the process's directory written since it started.
func (a *Adapter) newestSince(db *sql.DB, p proc.Proc, now time.Time, claimed map[string]bool) (thread, bool) {
	cwd := ""
	if a.Cwd != nil {
		cwd = a.Cwd(p.PID)
	}
	started := p.Started(now).Truncate(time.Second)
	for _, t := range threadsIn(db, cwd) {
		if !claimed[t.id] && !t.updated.Before(started) {
			return t, true
		}
	}
	return thread{}, false
}

// Discover resolves every thread named by arguments or open files before guessing the rest from their directories.
func (a *Adapter) Discover(procs []proc.Proc) []core.Session {
	now := a.Now()
	db, _ := a.db()
	ids := make([]string, len(procs))
	var wg sync.WaitGroup
	for i, p := range procs {
		if ids[i] = argThread(p.Args); ids[i] == "" {
			wg.Add(1)
			go func() {
				defer wg.Done()
				ids[i] = a.heldThread(db, p.PID)
			}()
		}
	}
	wg.Wait()
	claimed := map[string]bool{}
	for _, id := range ids {
		claimed[id] = id != ""
	}
	out := make([]core.Session, 0, len(procs))
	for i, p := range procs {
		out = append(out, a.read(db, p, ids[i], now, claimed))
	}
	return out
}

func (a *Adapter) read(db *sql.DB, p proc.Proc, id string, now time.Time, claimed map[string]bool) core.Session {
	s := core.Session{Harness: Name, PID: p.PID, TTY: p.TTY}
	var t thread
	var ok bool
	switch {
	case id != "":
		if db == nil {
			t, ok = thread{id: id}, true
		} else if t, ok = threadByID(db, id); !ok {
			t, ok = thread{id: id}, true
		}
	case db != nil:
		if t, ok = a.newestSince(db, p, now, claimed); ok {
			claimed[t.id] = true
		}
	}
	if !ok {
		if a.Cwd != nil {
			s.Cwd = a.Cwd(p.PID)
		}
		return s
	}
	s.ID, s.Title, s.Cwd, s.Branch = t.id, t.title, t.cwd, t.branch
	if s.Cwd == "" && a.Cwd != nil {
		s.Cwd = a.Cwd(p.PID)
	}
	if db != nil {
		s.Parent = parentOf(db, t.id)
		for _, c := range spawned(db, t.id) {
			s.Crew = append(s.Crew, a.member(c, now))
		}
	}
	if t.rollout == "" {
		return s
	}
	sc := a.scan(t.rollout, t.cwd)
	s.Facts = sc.Facts
	s.Task = firstNonEmpty(oneLine(t.task), sc.First)
	if s.Model == "" {
		s.Model = t.model
	}
	s.Since = sc.modified
	if sc.Open && now.Sub(sc.modified) < staleAfter {
		s.Status = core.Busy
		s.CheckScreen = true
	} else if !sc.modified.IsZero() {
		s.Status = core.Idle
	}
	return s
}

func (a *Adapter) member(c child, now time.Time) core.Member {
	m := core.Member{ID: c.id, Name: firstNonEmpty(c.nickname, c.role, core.Short(c.id)), Type: c.role, Description: c.path}
	if c.rollout != "" {
		sc := a.scan(c.rollout, c.cwd)
		m.Facts, m.Brief, m.Last = sc.Facts, sc.Brief, sc.modified
		if m.Model == "" {
			m.Model = c.model
		}
		switch {
		case c.open && sc.Open && now.Sub(sc.modified) < staleAfter:
			m.Status = core.Busy
		case c.open:
			m.Status = core.Idle
		default:
			m.Status, m.Cut = core.Done, sc.Open
		}
	}
	m.Task = firstNonEmpty(oneLine(c.task), firstLine(m.Brief), c.path)
	return m
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// SessionCwd is where thread session worked, from Codex's thread table.
func (a *Adapter) SessionCwd(session string) string {
	db, ok := a.db()
	if !ok {
		return ""
	}
	t, _ := threadByID(db, session)
	return t.cwd
}

func (a *Adapter) sqliteHome() string {
	if a.StateHome != "" {
		return a.StateHome
	}
	return profile.SQLiteHome(a.Home)
}

func (a *Adapter) ConversationArtifacts(id string) map[string]string {
	out := map[string]string{}
	if path := a.Transcript(id); path != "" {
		out[id] = path
	}
	if db, ok := a.db(); ok {
		for _, child := range spawned(db, id) {
			if path := a.Transcript(child.id); path != "" {
				out[child.id] = path
			}
		}
	}
	return out
}
