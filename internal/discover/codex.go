package discover

import (
	"database/sql"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"
)

type codexThread struct {
	id, rollout, title string
	updated            time.Time
}

func argCodexThread(args []string) string {
	for i, a := range args {
		if a == "resume" && i+1 < len(args) && len(args[i+1]) >= 32 {
			return args[i+1]
		}
	}
	return ""
}

// codexThreads lists the threads Codex recorded for cwd, newest first.
func (d *Discoverer) codexThreads(cwd string) []codexThread {
	matches, _ := filepath.Glob(filepath.Join(d.CodexHome, "state_*.sqlite"))
	if len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	db, err := sql.Open("sqlite", "file:"+matches[len(matches)-1]+"?mode=ro&"+url.Values{"_pragma": {"busy_timeout(2000)"}}.Encode())
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, rollout_path, COALESCE(NULLIF(name, ''), NULLIF(title, ''), first_user_message), updated_at
		FROM threads WHERE cwd = ? AND archived = 0 ORDER BY updated_at DESC`, cwd)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []codexThread
	for rows.Next() {
		var t codexThread
		var updated int64
		if rows.Scan(&t.id, &t.rollout, &t.title, &updated) == nil {
			t.updated = time.Unix(updated, 0)
			out = append(out, t)
		}
	}
	return out
}

// pickCodexThread prefers the thread named on the command line, then one Codex holds a writer lock on, then the newest written since the process started.
func (d *Discoverer) pickCodexThread(threads []codexThread, args []string, started time.Time, claimed map[string]bool) (codexThread, bool) {
	if id := argCodexThread(args); id != "" {
		for _, t := range threads {
			if t.id == id {
				return t, true
			}
		}
		return codexThread{id: id}, true
	}
	var free []codexThread
	for _, t := range threads {
		if !claimed[t.id] {
			free = append(free, t)
		}
	}
	for _, t := range free {
		if _, err := os.Stat(filepath.Join(d.CodexHome, "thread-writer-locks", t.id+".lock")); err == nil {
			return t, true
		}
	}
	for _, t := range free {
		if !t.updated.Before(started.Truncate(time.Second)) {
			return t, true
		}
	}
	return codexThread{}, false
}

func (d *Discoverer) readCodex(s *Session, p Proc, started, now time.Time, claimed map[string]bool) {
	t, ok := d.pickCodexThread(d.codexThreads(s.Cwd), p.Args, started, claimed)
	if !ok {
		s.Status, s.Since = settle(started, now), started
		return
	}
	s.ID, s.Title = t.id, t.title
	claimed[t.id] = true
	scan := d.scan(t.rollout, true)
	s.Since = scan.modified
	if scan.open && now.Sub(scan.modified) < StaleAfter {
		s.Status = Running
		return
	}
	s.Status = settle(s.Since, now)
}

// CodexTurn reports whether the Codex rollout at path has a task started with no task_complete after it, and when the rollout last changed.
func CodexTurn(path string) (open bool, modified time.Time) {
	scan := (&Discoverer{}).scan(path, true)
	return scan.open, scan.modified
}
