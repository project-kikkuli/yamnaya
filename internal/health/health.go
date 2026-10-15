// Package health measures whether ym keeps the owner's questions out of chat: how fast items get answered,
// how many go stale, how often agents reprint open questions in chat, and how long agents sit waiting on the owner.
package health

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/usage"
	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS ym_health_files (
	path TEXT PRIMARY KEY,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL,
	offset INTEGER NOT NULL,
	state TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_health_reprints (
	key TEXT PRIMARY KEY,
	path TEXT NOT NULL,
	day TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS ym_health_reprints_day ON ym_health_reprints(day);
CREATE TABLE IF NOT EXISTS ym_health_waits (
	key TEXT NOT NULL,
	day TEXT NOT NULL,
	path TEXT NOT NULL,
	kind TEXT NOT NULL,
	ms INTEGER NOT NULL,
	PRIMARY KEY (key, day)
) WITHOUT ROWID;
CREATE INDEX IF NOT EXISTS ym_health_waits_day ON ym_health_waits(day);
`

// Health reads Claude Code transcripts and Codex rollouts into per-day rows, reading each file only past where it last stopped.
type Health struct {
	ClaudeDir string
	CodexHome string
	db        *sql.DB
}

// Stats describes one scan.
type Stats struct {
	Files   int
	Changed int
	Bytes   int64
	Took    time.Duration
}

func Open(dbPath string) (*Health, error) {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Set("_txlock", "immediate")
	db, err := sql.Open("sqlite", "file:"+dbPath+"?"+q.Encode())
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("init health schema: %w", err)
	}
	claude, codex := usage.Dirs()
	return &Health{ClaudeDir: claude, CodexHome: codex, db: db}, nil
}

func (h *Health) Close() error { return h.db.Close() }

type file struct {
	usage.Transcript
	Offset int64
	State  state
	reset  bool
	found  found
	bytes  int64
}

func (h *Health) known() (map[string]*file, error) {
	rows, err := h.db.Query(`SELECT path, size, mtime, offset, state FROM ym_health_files`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*file{}
	for rows.Next() {
		f := &file{}
		var st string
		if err := rows.Scan(&f.Path, &f.Size, &f.MTime, &f.Offset, &st); err != nil {
			return nil, err
		}
		json.Unmarshal([]byte(st), &f.State)
		out[f.Path] = f
	}
	return out, rows.Err()
}

// Scan reads every transcript line appended since the last scan and stores what it adds.
func (h *Health) Scan() (Stats, error) {
	start := time.Now()
	var st Stats
	known, err := h.known()
	if err != nil {
		return st, err
	}
	srcs := usage.RootTranscripts(h.ClaudeDir, h.CodexHome)
	st.Files = len(srcs)
	var work []*file
	for _, s := range srcs {
		old := known[s.Path]
		switch {
		case old == nil:
			work = append(work, &file{Transcript: s})
		case old.Size == s.Size && old.MTime == s.MTime:
		case old.State.Skip:
			old.Size, old.MTime = s.Size, s.MTime
			work = append(work, old)
		case s.Size >= old.Offset:
			work = append(work, &file{Transcript: s, Offset: old.Offset, State: old.State})
		default:
			work = append(work, &file{Transcript: s, reset: true})
		}
	}
	st.Changed = len(work)
	parse(work)
	for _, f := range work {
		st.Bytes += f.bytes
	}
	if len(work) > 0 {
		if err := h.commit(work); err != nil {
			return st, err
		}
	}
	st.Took = time.Since(start)
	return st, nil
}

func parse(work []*file) {
	sort.Slice(work, func(i, j int) bool { return work[i].Size-work[i].Offset > work[j].Size-work[j].Offset })
	jobs := make(chan *file)
	var wg sync.WaitGroup
	for range max(1, runtime.NumCPU()-1) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for f := range jobs {
				if f.State.Skip {
					continue
				}
				from := f.Offset
				line := f.State.claudeLine
				if f.Harness != "claude" {
					line = f.State.codexLine
					if !f.State.Seen && from == 0 {
						if head, err := firstLine(f.Path); err == nil {
							line(head, &f.found)
						}
						if f.State.Skip {
							f.Offset, f.bytes = f.Size, f.Size
							continue
						}
					}
				}
				f.Offset, _ = usage.ReadLines(f.Path, from, func(l []byte) {
					if !f.State.Skip {
						line(l, &f.found)
					}
				})
				f.bytes = f.Offset - from
			}
		}()
	}
	for _, f := range work {
		jobs <- f
	}
	close(jobs)
	wg.Wait()
}

func (h *Health) commit(work []*file) error {
	tx, err := h.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, f := range work {
		if !f.reset {
			continue
		}
		for _, q := range []string{`DELETE FROM ym_health_reprints WHERE path = ?`, `DELETE FROM ym_health_waits WHERE path = ?`} {
			if _, err := tx.Exec(q, f.Path); err != nil {
				return err
			}
		}
	}
	rep, err := tx.Prepare(`INSERT OR IGNORE INTO ym_health_reprints (key, path, day) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer rep.Close()
	wt, err := tx.Prepare(`INSERT OR IGNORE INTO ym_health_waits (key, day, path, kind, ms) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer wt.Close()
	fl, err := tx.Prepare(`INSERT INTO ym_health_files (path, size, mtime, offset, state) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET size = excluded.size, mtime = excluded.mtime, offset = excluded.offset, state = excluded.state`)
	if err != nil {
		return err
	}
	defer fl.Close()
	for _, f := range work {
		for _, r := range f.found.reprints {
			if _, err := rep.Exec(r.Key, f.Path, r.Day); err != nil {
				return err
			}
		}
		for _, w := range f.found.waits {
			k := key(w.Kind, w.Start.UTC().Format(time.RFC3339Nano), w.End.UTC().Format(time.RFC3339Nano))
			for _, p := range byDay(w.Start, w.End) {
				if _, err := wt.Exec(k, p.day, f.Path, w.Kind, p.span.Milliseconds()); err != nil {
					return err
				}
			}
		}
		st, _ := json.Marshal(f.State)
		if _, err := fl.Exec(f.Path, f.Size, f.MTime, f.Offset, string(st)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func firstLine(path string) ([]byte, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return bufio.NewReaderSize(fh, 1<<16).ReadBytes('\n')
}

type piece struct {
	day  string
	span time.Duration
}

// byDay splits [start, end) at local midnights.
func byDay(start, end time.Time) []piece {
	var out []piece
	for at := start.Local(); at.Before(end); {
		y, m, d := at.Date()
		next := time.Date(y, m, d+1, 0, 0, 0, 0, time.Local)
		stop := next
		if end.Before(stop) {
			stop = end
		}
		out = append(out, piece{day: at.Format(time.DateOnly), span: stop.Sub(at)})
		at = next
	}
	return out
}
