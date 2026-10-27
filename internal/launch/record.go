package launch

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/profile"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Record is one agent session ym launched: where it runs and how to bring it back.
type Record struct {
	RootValue        string    `json:"root_value,omitempty"`
	RootSet          bool      `json:"root_set,omitempty"`
	SQLiteEnv        string    `json:"sqlite_env,omitempty"`
	SQLiteEnvSet     bool      `json:"sqlite_env_set,omitempty"`
	Handoff          string    `json:"handoff,omitempty"`
	AttachedAt       time.Time `json:"attached_at,omitzero"`
	Account          string    `json:"account,omitempty"`
	NativePID        int       `json:"native_pid,omitempty"`
	Dedicated        bool      `json:"dedicated,omitempty"`
	Model            string    `json:"model,omitempty"`
	Profile          string    `json:"profile,omitempty"`
	ConfigRoot       string    `json:"config_root,omitempty"`
	SQLiteRoot       string    `json:"sqlite_root,omitempty"`
	ConfigExplicit   bool      `json:"config_explicit,omitempty"`
	SecureStorage    string    `json:"secure_storage,omitempty"`
	SecureStorageSet bool      `json:"secure_storage_set,omitempty"`
	Key              string    `json:"key"`
	Harness          string    `json:"harness"`
	Session          string    `json:"session,omitempty"`
	Parent           string    `json:"parent,omitempty"`
	Prompt           string    `json:"prompt"`
	Worktree         Worktree  `json:"worktree"`
	Head             string    `json:"head,omitempty"`
	// Repo is the repository's identity across clones, so a revive finds another clone once this one is gone.
	Repo    string    `json:"repo,omitempty"`
	Started time.Time `json:"started"`
	// Adopted records a session ym moved in from another terminal; its directory is not ym's to remove.
	Adopted bool `json:"adopted,omitempty"`
	// Seen is when the board last saw this session ym never ran running elsewhere, kept so ym revive resumes it once it ends.
	Seen time.Time `json:"seen,omitzero"`
	// Killed is when ym kill ended the agent, so its exit waits out the same day of grace a signal death gets.
	Killed time.Time `json:"killed,omitzero"`
	// Landed is when ym ended the session and removed its worktree; ym revive recreates both from Worktree and Head.
	Landed time.Time `json:"landed,omitzero"`
	// Recovery is the work ym kill bundled out of the worktree before removing it; ym revive puts it back.
	Recovery Recovery `json:"recovery,omitzero"`
	// Goal is the goal or chief whose kill ended the session, so reviving it revives the session too.
	Goal string `json:"goal,omitempty"`
	// Blocked is why the last automatic landing refused, with Stamp the worktree's Stamp then; it is not tried again until the
	// worktree changes.
	Blocked string `json:"blocked,omitempty"`
	Stamp   string `json:"stamp,omitempty"`
	// Running is set while ym last saw its agent run; Revive once ym's tmux server went away under it, as a restart of the host
	// leaves it, so it resumes instead of landing.
	Running bool `json:"running,omitempty"`
	// Starting is set while ym new has saved the record and not yet seen its tmux session start.
	Starting bool `json:"starting,omitempty"`
	Revive   bool `json:"revive,omitempty"`
}

func (r Record) IsLanded() bool { return !r.Landed.IsZero() }

// Records keeps one JSON file per launch under Dir.
type Records struct{ Dir string }

func (r Records) path(key string) string { return filepath.Join(r.Dir, key+".json") }

func (r Records) Save(rec Record) error {
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	return profile.Write(r.path(rec.Key), data)
}

// Has reports whether a record, landed or not, holds key.
func (r Records) Has(key string) bool {
	_, err := os.Stat(r.path(key))
	return err == nil
}

func (r Records) Delete(key string) error {
	err := os.Remove(r.path(key))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

// All lists every record, oldest first.
func (r Records) All() ([]Record, error) {
	files, err := filepath.Glob(filepath.Join(r.Dir, "*.json"))
	if err != nil {
		return nil, err
	}
	var out []Record
	for _, f := range files {
		if rec, ok := read(f); ok {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}

type cached struct {
	size     int64
	modified time.Time
	rec      Record
	ok       bool
}

var (
	cacheMu sync.Mutex
	cache   = map[string]cached{}
)

// read parses a record file, again only after its size or modification time changes.
func read(path string) (Record, bool) {
	info, err := os.Stat(path)
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if err != nil {
		delete(cache, path)
		return Record{}, false
	}
	if c, ok := cache[path]; ok && c.size == info.Size() && c.modified.Equal(info.ModTime()) {
		return c.rec, c.ok
	}
	c := cached{size: info.Size(), modified: info.ModTime()}
	if data, err := os.ReadFile(path); err == nil {
		c.ok = json.Unmarshal(data, &c.rec) == nil && c.rec.Key != ""
	}
	cache[path] = c
	return c.rec, c.ok
}

// Find is Lookup without its error; ambiguous references never select a record.
func (r Records) Find(ref string) (Record, bool) {
	rec, err := r.Lookup(ref)
	return rec, err == nil
}

func (r Records) Lookup(ref string) (Record, error) {
	all, err := r.All()
	if err != nil {
		return Record{}, err
	}
	scope := ""
	if before, after, ok := strings.Cut(ref, "@"); ok {
		scope, ref = before, after
	}
	var exact []Record
	var ids []string
	var candidates []Record
	for _, rec := range all {
		if scope != "" && rec.Harness+":"+profile.ID(rec.Profile) != scope {
			continue
		}
		candidates = append(candidates, rec)
		if rec.Key == ref || rec.Worktree.Branch == ref {
			exact = append(exact, rec)
		}
		ids = append(ids, rec.Session)
	}
	if len(exact) == 0 {
		id, err := core.SessionRef(ref, ids)
		if err != nil {
			return Record{}, err
		}
		for _, rec := range candidates {
			if rec.Session == id {
				exact = append(exact, rec)
			}
		}
	}
	for _, rec := range exact[1:] {
		if rec.Harness != exact[0].Harness || profile.ID(rec.Profile) != profile.ID(exact[0].Profile) {
			return Record{}, fmt.Errorf("%w %q: use harness:profile@session", core.ErrAmbiguous, ref)
		}
	}

	var live []Record
	for _, rec := range exact {
		if !rec.IsLanded() {
			live = append(live, rec)
		}
	}
	if len(live) > 0 {
		exact = live
	}
	for _, rec := range exact[1:] {
		if rec.Session != exact[0].Session {
			return Record{}, fmt.Errorf("%w %q: launches %s and %s", core.ErrAmbiguous, ref, exact[0].Key, rec.Key)
		}
	}
	return exact[0], nil
}

// Landing is one line of the landing log: a session ym landed, what it ran in, and why it was safe to remove.
type Landing struct {
	At       time.Time `json:"at"`
	Key      string    `json:"key"`
	Harness  string    `json:"harness"`
	Session  string    `json:"session,omitempty"`
	Worktree Worktree  `json:"worktree"`
	Head     string    `json:"head,omitempty"`
	Why      string    `json:"why"`
}

// Log appends l to the JSON-lines file at path.
func Log(path string, l Landing) error {
	data, err := json.Marshal(l)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func slugWords(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)))
	})
}

// NormalizeSlug makes a typed worktree name a valid branch name: lowercase ASCII words joined by dashes, so any
// character git refuses in a ref (spaces, slashes, dots, a leading dash, a .lock suffix) becomes a dash or vanishes.
// It is empty when nothing usable is left.
func NormalizeSlug(name string) string {
	s := strings.Join(slugWords(name), "-")
	if len(s) > 48 {
		s = strings.TrimRight(s[:48], "-")
	}
	return s
}

// Slug turns a prompt into a short branch name: its first words, lowercased, joined by dashes.
func Slug(prompt string) string {
	words := slugWords(prompt)
	s := ""
	for _, w := range words {
		next := w
		if s != "" {
			next = s + "-" + w
		}
		if len(next) > 32 {
			if s == "" {
				s = w[:32]
			}
			break
		}
		s = next
		if strings.Count(s, "-") == 3 {
			break
		}
	}
	if s == "" {
		return "agent"
	}
	return s
}
