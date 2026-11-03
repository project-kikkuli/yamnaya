package launch

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// regenerableDirs and regenerableFiles name the git-ignored entries a build or install recreates.
var regenerableDirs = map[string]bool{
	"node_modules": true, ".venv": true, "target": true, "__pycache__": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".next": true, ".turbo": true, ".parcel-cache": true, ".gradle": true, ".tox": true, ".nox": true,
	".svelte-kit": true,
}

var regenerableFiles = []string{"*.pyc", "*.pyo", "*.tsbuildinfo", ".DS_Store", ".eslintcache"}

// Regenerable reports whether a path git lists as ignored, with a trailing slash when a directory, is on the allowlist.
func Regenerable(entry string) bool { return regenerableName(entry) != "" }

// regenerableName is the allowlisted directory the entry lies in or is, else the allowlisted file it is, else "".
func regenerableName(entry string) string {
	parts := strings.Split(strings.TrimSuffix(entry, "/"), "/")
	for _, part := range parts {
		if regenerableDirs[part] {
			return part
		}
	}
	base := parts[len(parts)-1]
	if strings.HasSuffix(entry, "/") {
		return ""
	}
	if i := slices.IndexFunc(regenerableFiles, func(p string) bool {
		ok, _ := filepath.Match(p, base)
		return ok
	}); i >= 0 {
		return regenerableFiles[i]
	}
	return ""
}

// ignored lists the git-ignored entries of the worktree at path, without descending into an allowlisted one. Git folds a directory
// holding only ignored files into one entry, hiding a __pycache__ inside it, so such a directory is listed by what it holds.
func ignored(path string) ([]string, error) {
	out, err := look(path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	var all []string
	for _, e := range strings.FieldsFunc(out, func(r rune) bool { return r == 0 }) {
		if strings.HasSuffix(e, "/") && !Regenerable(e) {
			all = unfold(path, e, all)
		} else {
			all = append(all, e)
		}
	}
	return all, nil
}

func unfold(root, dir string, all []string) []string {
	entries, err := os.ReadDir(filepath.Join(root, dir))
	if err != nil {
		return append(all, dir)
	}
	for _, c := range entries {
		e := dir + c.Name()
		if c.IsDir() {
			e += "/"
		}
		if c.IsDir() && !Regenerable(e) {
			all = unfold(root, e, all)
		} else {
			all = append(all, e)
		}
	}
	return all
}

// unregenerable lists the git-ignored entries of the worktree at path a build could not recreate.
func unregenerable(path string) ([]string, error) {
	all, err := ignored(path)
	return slices.DeleteFunc(all, Regenerable), err
}

// Stripped is what a strip took out of one worktree: how many entries of each name, and their bytes when measured.
type Stripped struct {
	Sweep   Sweep
	Entries map[string]int
	Size    int64
	Err     error
}

// Why is one line naming what was stripped, biggest count first.
func (s Stripped) Why() string {
	names := slices.Collect(maps.Keys(s.Entries))
	sort.Slice(names, func(i, j int) bool {
		if s.Entries[names[i]] != s.Entries[names[j]] {
			return s.Entries[names[i]] > s.Entries[names[j]]
		}
		return names[i] < names[j]
	})
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = fmt.Sprintf("%s x%d", n, s.Entries[n])
	}
	return strings.Join(parts, ", ")
}

// StripIdle is how long a kept worktree goes untouched before a reclaim pass strips it.
const StripIdle = 6 * time.Hour

// Strippable reports whether a worktree gc decided on may lose its regenerable files: gc kept it, it went StripIdle
// untouched and no process works in it. Locked and unreadable worktrees are never touched.
func Strippable(w Sweep) bool {
	return w.Keep != "" && w.Keep != KeepLocked && w.Keep != KeepFailing && w.Keep != KeepInUse && w.User == "" && w.Idle >= StripIdle
}

// StripIgnored removes the allowlisted git-ignored entries of the worktree w, confirming each with git check-ignore, and leaves
// the worktree's own mtime as it was so the strip does not count as a touch. With dry it only measures.
func StripIgnored(w Sweep, dry bool) Stripped {
	out := Stripped{Sweep: w, Entries: map[string]int{}}
	all, err := ignored(w.Path)
	if err != nil {
		out.Err = err
		return out
	}
	all = slices.DeleteFunc(all, func(e string) bool { return !Regenerable(e) })
	if len(all) == 0 {
		return out
	}
	confirmed, err := gitEnv(w.Path, nil, []byte(strings.Join(all, "\x00")+"\x00"), "-c", "core.fsmonitor=false", "check-ignore", "--stdin", "-z")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return out
	}
	if err != nil {
		out.Err = err
		return out
	}
	before, statErr := os.Stat(w.Path)
	for _, e := range strings.Split(confirmed, "\x00") {
		if e == "" {
			continue
		}
		p := filepath.Join(w.Path, e)
		if dry {
			out.Size += Size(p)
		} else if err := os.RemoveAll(p); err != nil {
			out.Err = err
			continue
		}
		out.Entries[regenerableName(e)]++
	}
	if !dry && statErr == nil {
		os.Chtimes(w.Path, before.ModTime(), before.ModTime())
	}
	return out
}
