package profile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/nativefile"
	"io"
	"os"
	"path/filepath"
)

type Import struct {
	Origins       []Origin        `json:"origins"`
	Start         int64           `json:"start"`
	End           int64           `json:"end"`
	Hash          string          `json:"sha256"`
	SourceRoot    string          `json:"source_root"`
	SourceProfile string          `json:"source_profile"`
	SourceAccount string          `json:"source_account,omitempty"`
	SourcePath    string          `json:"source_path"`
	Conversation  string          `json:"conversation"`
	Rollout       string          `json:"rollout,omitempty"`
	Seed          json.RawMessage `json:"codex_seed,omitempty"`
}

func importPath(dir, path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return filepath.Join(dir, "imports", hex.EncodeToString(sum[:])+".json")
}
func Imports(dir, path string) ([]Import, error) {
	b, err := os.ReadFile(importPath(dir, path))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ranges []Import
	err = json.Unmarshal(b, &ranges)
	return ranges, err
}
func SaveImports(dir, path string, ranges []Import) error {
	b, err := json.Marshal(ranges)
	if err != nil {
		return err
	}
	return Write(importPath(dir, path), b)
}
func HashRange(path string, start, end int64) (string, error) {
	f, err := nativefile.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	n, err := io.CopyN(h, f, end-start)
	if err != nil {
		return "", err
	}
	if n != end-start {
		return "", io.ErrUnexpectedEOF
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func ValidateImports(path string, ranges []Import) error {
	var end int64
	for _, r := range ranges {
		if r.Start < end || r.End < r.Start {
			return fmt.Errorf("invalid imported ranges: %s", path)
		}
		hash, err := HashRange(path, r.Start, r.End)
		if err != nil {
			return err
		}
		if hash != r.Hash {
			return fmt.Errorf("imported history changed in %s", path)
		}
		end = r.End
	}
	return nil
}

type Origin struct {
	Start   int64  `json:"start"`
	End     int64  `json:"end"`
	Root    string `json:"root"`
	Profile string `json:"profile"`
	Account string `json:"account,omitempty"`
	Path    string `json:"path"`
}

func Origins(dir, path string, p Profile, start, end int64) ([]Origin, error) {
	ranges, err := Imports(dir, path)
	if err != nil {
		return nil, err
	}
	var out []Origin
	at := start
	for _, r := range ranges {
		if r.End <= start || r.Start >= end {
			continue
		}
		if at < r.Start {
			out = append(out, Origin{Start: at, End: r.Start, Root: p.Root, Profile: p.ID, Account: p.Account, Path: path})
			at = r.Start
		}
		origins := r.Origins
		if len(origins) == 0 {
			origins = []Origin{{Start: r.Start, End: r.End, Root: r.SourceRoot, Profile: r.SourceProfile, Account: r.SourceAccount, Path: r.SourcePath}}
		}
		for _, o := range origins {
			o.Start = max(o.Start, start)
			o.End = min(o.End, end)
			if o.Start < o.End {
				out = append(out, o)
				at = o.End
			}
		}
	}
	if at < end {
		out = append(out, Origin{Start: at, End: end, Root: p.Root, Profile: p.ID, Account: p.Account, Path: path})
	}
	return out, nil
}
