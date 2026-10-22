package launch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Record is one agent session ym launched: where it runs and how to bring it back.
type Record struct {
	Key      string    `json:"key"`
	Harness  string    `json:"harness"`
	Session  string    `json:"session,omitempty"`
	Parent   string    `json:"parent,omitempty"`
	Prompt   string    `json:"prompt"`
	Worktree Worktree  `json:"worktree"`
	Head     string    `json:"head,omitempty"`
	Started  time.Time `json:"started"`
	// Adopted records a session ym moved in from another terminal; its directory is not ym's to remove.
	Adopted bool `json:"adopted,omitempty"`
	// Killed is when ym kill ended the agent, so its exit waits out the same day of grace a signal death gets.
	Killed time.Time `json:"killed,omitzero"`
	// Landed is when ym ended the session and removed its worktree; ym revive recreates both from Worktree and Head.
	Landed time.Time `json:"landed,omitzero"`
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
	tmp := r.path(rec.Key) + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, r.path(rec.Key))
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

// Find matches ref against keys, branch slugs and session ids; a session id may be given by a prefix of six or more.
// A record not yet landed wins over a landed one.
func (r Records) Find(ref string) (Record, bool) {
	all, _ := r.All()
	var landed *Record
	for i, rec := range all {
		if rec.Key == ref || rec.Worktree.Branch == ref || (rec.Session != "" && (rec.Session == ref || (len(ref) >= 6 && strings.HasPrefix(rec.Session, ref)))) {
			if !rec.IsLanded() {
				return rec, true
			}
			if landed == nil {
				landed = &all[i]
			}
		}
	}
	if landed != nil {
		return *landed, true
	}
	return Record{}, false
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
