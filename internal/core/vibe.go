package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// VibeStale is how long a vibe reads as current.
const VibeStale = 2 * time.Hour

// Vibe is an agent's read of how its mission is going, as a place from North Dakota (rough) to Hawaii (great).
type Vibe struct {
	Place string
	Why   string
	// Session set the vibe; Target is the session or project it was set on, the setter's own when empty.
	Session string
	Harness string
	Target  string
	At      time.Time
}

func vibes(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS ym_vibes (
		seq INTEGER PRIMARY KEY AUTOINCREMENT,
		place TEXT NOT NULL,
		why TEXT NOT NULL DEFAULT '',
		session TEXT NOT NULL DEFAULT '',
		harness TEXT NOT NULL DEFAULT '',
		target TEXT NOT NULL DEFAULT '',
		at TEXT NOT NULL
	)`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) AddVibe(v Vibe) (Vibe, error) {
	v.Place, v.Why = oneLine(v.Place), oneLine(v.Why)
	if v.Place == "" {
		return v, errors.New("a vibe needs a place")
	}
	if v.Session == "" && v.Target == "" {
		return v, errors.New("a vibe needs a session or project")
	}
	v.At = time.Now().UTC()
	_, err := s.db.Exec(`INSERT INTO ym_vibes (place, why, session, harness, target, at) VALUES (?, ?, ?, ?, ?, ?)`,
		v.Place, v.Why, v.Session, v.Harness, v.Target, v.At.Format(time.RFC3339Nano))
	return v, err
}

// Vibes lists every vibe, newest first.
func (s *Store) Vibes() ([]Vibe, error) {
	rows, err := s.db.Query(`SELECT place, why, session, harness, target, at FROM ym_vibes ORDER BY seq DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Vibe
	for rows.Next() {
		var v Vibe
		var at sql.NullString
		if err := rows.Scan(&v.Place, &v.Why, &v.Session, &v.Harness, &v.Target, &at); err != nil {
			return nil, err
		}
		v.At = parseTime(at)
		out = append(out, v)
	}
	return out, rows.Err()
}

func (v Vibe) on() string {
	if v.Target != "" {
		return v.Target
	}
	return v.Session
}

// VibesOf is the vibes set on a project, newest first.
func (b Board) VibesOf(all []Vibe, project string) []Vibe {
	var out []Vibe
	for _, v := range all {
		if b.ProjectOf(v.on()) == project {
			out = append(out, v)
		}
	}
	return out
}

// Vibe is the project's latest vibe.
func (p Project) Vibe() (Vibe, bool) {
	if len(p.Vibes) == 0 {
		return Vibe{}, false
	}
	return p.Vibes[0], true
}

// VibeStale reports whether the latest vibe is old or every session of the project has ended.
func (p Project) VibeStale(now time.Time) bool {
	v, ok := p.Vibe()
	if !ok || now.Sub(v.At) > VibeStale {
		return true
	}
	for _, s := range p.Sessions {
		if s.Status != Done {
			return false
		}
	}
	return true
}

// Places is the project's place changes, newest first, at most n.
func (p Project) Places(n int) []Vibe {
	var out []Vibe
	for _, v := range p.Vibes {
		if len(out) == n {
			break
		}
		if len(out) == 0 || !strings.EqualFold(out[len(out)-1].Place, v.Place) {
			out = append(out, v)
		}
	}
	return out
}

// VibeTarget is where a vibe set with -p ref goes: the mission ref names, else the setter's own when caller is set, as ym item add files against the caller whatever -p says.
func (b Board) VibeTarget(ref, caller string) (string, error) {
	key, err := b.ProjectKey(ref)
	if err != nil && caller != "" {
		return "", nil
	}
	return key, err
}

// VibeNudge asks the caller to set its mission's vibe while the mission has none or only ones older than VibeStale; "" while one is fresh.
func (c *Core) VibeNudge(cl Caller) (string, error) {
	all, err := c.Store.Vibes()
	if err != nil {
		return "", err
	}
	lin, err := c.Store.Lineage()
	if err != nil {
		return "", err
	}
	// A fresh vibe on the caller's own spawn chain settles it without the slow full board.
	chain := c.spawnChain(cl)
	root := projectOf(cl.Session, chain, lin)
	for _, v := range all {
		if c.now().Sub(v.At) > VibeStale {
			break
		}
		if projectOf(v.on(), chain, lin) == root {
			return "", nil
		}
	}
	b, err := c.Board()
	if err != nil {
		return "", err
	}
	const run = `run ym vibe "<place>" --why "..."`
	vs := b.VibesOf(all, b.ProjectOf(cl.Session))
	if len(vs) == 0 {
		return "none yet, " + run, nil
	}
	age := b.At.Sub(vs[0].At)
	if age <= VibeStale {
		return "", nil
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh old, %s", int(age.Hours()), run), nil
	}
	return fmt.Sprintf("%dd old, %s", int(age.Hours()/24), run), nil
}

// spawnChain links the caller to each agent session running above it in the process tree, as the board does.
func (c *Core) spawnChain(cl Caller) map[string]string {
	chain := map[string]string{}
	if cl.PID == 0 {
		return chain
	}
	procs, err := c.Procs()
	if err != nil {
		return chain
	}
	child := cl.Session
	for _, p := range proc.Index(procs).Ancestors(cl.PID) {
		for _, h := range c.Harnesses {
			if !h.Owns(p) {
				continue
			}
			if id := h.SessionID(p); p.TTY != "" && id != "" && id != child {
				chain[child], child = id, id
			}
			break
		}
	}
	return chain
}
