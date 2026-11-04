package launch

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"
)

// regenerableDirs and regenerableFiles name the git-ignored entries a build or install recreates. A directory counts only
// when git itself reports the whole directory as ignored, so a stray file under a like-named tracked directory never does.
var regenerableDirs = map[string]bool{
	"node_modules": true, ".venv": true, "target": true, "__pycache__": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".next": true, ".turbo": true, ".parcel-cache": true, ".gradle": true, ".tox": true, ".nox": true,
	".svelte-kit": true,
}

var regenerableFiles = []string{"*.pyc", "*.pyo", "*.tsbuildinfo", ".DS_Store", ".eslintcache"}

// targetMarkers are the build files that make a sibling target directory a build's output rather than someone's folder.
var targetMarkers = []string{"Cargo.toml", "pom.xml"}

// holdsGit reports whether anything at any depth under path is a .git, so it holds another repository or worktree.
func holdsGit(path string) bool {
	found := false
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			found = true
			return filepath.SkipAll
		}
		if d.Name() == ".git" {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// ignoredEntries lists the git-ignored entries of the worktree at path as git reports them, folding a virtualenv that
// ignores itself, as uv's do, back into one directory.
func ignoredEntries(path string) ([]string, error) {
	out, err := look(path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z")
	if err != nil {
		return nil, err
	}
	var all []string
	venvs := map[string]bool{}
	for _, e := range strings.FieldsFunc(out, func(r rune) bool { return r == 0 }) {
		if venv, ok := selfIgnoringVenv(path, e); ok {
			if !venvs[venv] {
				all = append(all, venv)
			}
			venvs[venv] = true
			continue
		}
		all = append(all, e)
	}
	return all, nil
}

func selfIgnoringVenv(root, entry string) (string, bool) {
	dir, _, ok := strings.Cut("/"+entry, "/.venv/")
	if !ok || strings.TrimSuffix(entry, "/") == strings.TrimPrefix(dir+"/.venv", "/") {
		return "", false
	}
	venv := strings.TrimPrefix(dir+"/.venv/", "/")
	if _, err := os.Stat(filepath.Join(root, venv, "pyvenv.cfg")); err != nil {
		return "", false
	}
	if rule, err := os.ReadFile(filepath.Join(root, venv, ".gitignore")); err != nil || strings.TrimSpace(string(rule)) != "*" {
		return "", false
	}
	if tracked, err := look(root, "ls-files", "--", venv); err != nil || tracked != "" {
		return "", false
	}
	return venv, true
}

// regenerable reports whether an ignored entry of the worktree at root is safe to delete: an allowlisted whole directory
// holding no other repository or worktree, or an allowlisted file.
func regenerable(root, entry string) bool {
	base := filepath.Base(strings.TrimSuffix(entry, "/"))
	if !strings.HasSuffix(entry, "/") {
		return slices.ContainsFunc(regenerableFiles, func(p string) bool {
			ok, _ := filepath.Match(p, base)
			return ok
		})
	}
	if !regenerableDirs[base] {
		return false
	}
	if base == "target" && !slices.ContainsFunc(targetMarkers, func(m string) bool {
		_, err := os.Stat(filepath.Join(root, filepath.Dir(strings.TrimSuffix(entry, "/")), m))
		return err == nil
	}) {
		return false
	}
	return !holdsGit(filepath.Join(root, entry))
}

// keepContents says why what lies ignored in the worktree at path must stay: a repository nested at any depth always, and with
// strict anything off the allowlist. Removing a worktree takes its ignored files with it.
func keepContents(path string, strict bool) (string, string) {
	all, err := ignoredEntries(path)
	if err != nil {
		return KeepFailing, err.Error()
	}
	for _, e := range all {
		if strings.HasSuffix(e, "/") && holdsGit(filepath.Join(path, e)) {
			return KeepNested, "holds a repository under " + e
		}
	}
	if strict {
		for _, e := range all {
			if !regenerable(path, e) {
				return KeepIgnored, "holds git-ignored " + e
			}
		}
	}
	if stage, err := look(path, "ls-files", "--stage"); err != nil {
		return KeepFailing, err.Error()
	} else if strings.HasPrefix(stage, "160000 ") || strings.Contains(stage, "\n160000 ") {
		return KeepModules, "git cannot remove a worktree with submodules"
	}
	return "", ""
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
	all, err := ignoredEntries(w.Path)
	if err != nil {
		out.Err = err
		return out
	}
	all = slices.DeleteFunc(all, func(e string) bool { return !regenerable(w.Path, e) })
	if len(all) == 0 {
		return out
	}
	var asked []string
	for _, e := range all {
		if !strings.HasSuffix(e, "/.venv/") && e != ".venv/" {
			asked = append(asked, e)
		}
	}
	confirmed := all[:0:0]
	if len(asked) > 0 {
		said, err := gitEnv(w.Path, nil, []byte(strings.Join(asked, "\x00")+"\x00"), "-c", "core.fsmonitor=false", "check-ignore", "--stdin", "-z")
		var exit *exec.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
			out.Err = err
			return out
		}
		confirmed = strings.FieldsFunc(said, func(r rune) bool { return r == 0 })
	}
	for _, e := range all {
		if !slices.Contains(asked, e) {
			confirmed = append(confirmed, e)
		}
	}
	before, statErr := os.Stat(w.Path)
	for _, e := range confirmed {
		p := filepath.Join(w.Path, e)
		if dry {
			out.Size += Size(p)
		} else if err := os.RemoveAll(p); err != nil {
			out.Err = err
			continue
		}
		out.Entries[filepath.Base(strings.TrimSuffix(e, "/"))]++
	}
	if !dry && statErr == nil {
		os.Chtimes(w.Path, before.ModTime(), before.ModTime())
	}
	return out
}
