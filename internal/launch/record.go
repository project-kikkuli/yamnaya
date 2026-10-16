package launch

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
}

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
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec Record
		if json.Unmarshal(data, &rec) == nil && rec.Key != "" {
			out = append(out, rec)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Started.Before(out[j].Started) })
	return out, nil
}

// Find matches ref against keys, branch slugs and session ids; a session id may be given by a prefix of six or more.
func (r Records) Find(ref string) (Record, bool) {
	all, _ := r.All()
	for _, rec := range all {
		if rec.Key == ref || rec.Worktree.Branch == ref || (rec.Session != "" && (rec.Session == ref || (len(ref) >= 6 && strings.HasPrefix(rec.Session, ref)))) {
			return rec, true
		}
	}
	return Record{}, false
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
