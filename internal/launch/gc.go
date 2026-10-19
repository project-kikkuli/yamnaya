package launch

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Idle is how long a worktree goes untouched before gc removes it.
const Idle = 3 * 24 * time.Hour

// Why gc leaves a worktree in place.
const (
	KeepDirty   = "dirty"
	KeepLocal   = "local-only commits"
	KeepActive  = "active"
	KeepInUse   = "in use"
	KeepLocked  = "locked"
	KeepFailing = "unreadable"
)

// Sweep is what gc decided about one linked worktree: Keep is empty when it removes it.
type Sweep struct {
	Path   string
	Branch string
	Keep   string
	Why    string
	Size   int64
}

// Swept is one repository's gc: its worktrees, the branches it deleted, and what failed.
type Swept struct {
	Root     string
	Trees    []Sweep
	Branches []string
	Errors   []string
}

// Keep says why gc leaves a worktree, or "" when removing it loses nothing and nobody works in it.
func Keep(dirty bool, local int, idle time.Duration, user string) (string, string) {
	switch {
	case dirty:
		return KeepDirty, "has uncommitted or untracked changes"
	case local > 0:
		return KeepLocal, fmt.Sprintf("%d commits on no remote", local)
	case idle < Idle:
		return KeepActive, "touched " + idle.Round(time.Hour).String() + " ago"
	case user != "":
		return KeepInUse, "a process works in " + user
	}
	return "", ""
}

// look runs a read-only git command that neither rewrites the index nor starts an fsmonitor daemon, so gc leaves no trace.
func look(dir string, args ...string) (string, error) {
	return git(dir, append([]string{"--no-optional-locks", "-c", "core.fsmonitor=false"}, args...)...)
}

type linked struct {
	path, branch string
	locked, main bool
}

// worktrees lists the worktrees git knows for the repository at root, its main checkout first.
func worktrees(root string) ([]linked, error) {
	out, err := look(root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	var all []linked
	for i, block := range strings.Split(out, "\n\n") {
		var w linked
		for _, line := range strings.Split(block, "\n") {
			key, val, _ := strings.Cut(line, " ")
			switch key {
			case "worktree":
				w.path = val
			case "branch":
				w.branch = strings.TrimPrefix(val, "refs/heads/")
			case "locked", "prunable":
				w.locked = true
			case "bare":
				w.main = true
			}
		}
		w.main = w.main || i == 0
		if w.path != "" {
			all = append(all, w)
		}
	}
	return all, nil
}

// Base is the remote default branch merges are judged against: origin/HEAD, else origin/main or origin/master, else "".
func Base(root string) string {
	if ref, err := git(root, "rev-parse", "--abbrev-ref", "origin/HEAD"); err == nil && ref != "origin/HEAD" {
		return ref
	}
	for _, ref := range []string{"origin/main", "origin/master"} {
		if _, err := git(root, "rev-parse", "--verify", "--quiet", ref); err == nil {
			return ref
		}
	}
	return ""
}

// touched is the newest change to the worktree's directory, its HEAD or its newest reflog entry. The index and the
// reflog file's own mtime are left out because any git status rewrites the one and git gc's reflog expiry the other.
func touched(path string) time.Time {
	var newest time.Time
	see := func(t time.Time) {
		if t.After(newest) {
			newest = t
		}
	}
	stat := func(p string) {
		if fi, err := os.Stat(p); err == nil {
			see(fi.ModTime())
		}
	}
	stat(path)
	if b, err := os.ReadFile(filepath.Join(path, ".git")); err == nil {
		if admin, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: "); ok {
			stat(filepath.Join(admin, "HEAD"))
			see(lastEntry(filepath.Join(admin, "logs", "HEAD")))
		}
	}
	return newest
}

// lastEntry is when the reflog at path last recorded a move: each line is "old new who <unix> <zone>\tmessage".
func lastEntry(path string) time.Time {
	b, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}
	}
	lines := strings.Split(strings.TrimRight(string(b), "\n"), "\n")
	who, _, _ := strings.Cut(lines[len(lines)-1], "\t")
	f := strings.Fields(who)
	if len(f) < 2 {
		return time.Time{}
	}
	sec, err := strconv.ParseInt(f[len(f)-2], 10, 64)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

// Size is the bytes of every file under path, not following links.
func Size(path string) int64 {
	var n int64
	filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if fi, err := d.Info(); err == nil {
				n += fi.Size()
			}
		}
		return nil
	})
	return n
}

func inside(path string, cwds []string) string {
	for _, c := range cwds {
		if c == path || strings.HasPrefix(c, path+string(filepath.Separator)) {
			return c
		}
	}
	return ""
}

// GC removes the linked worktrees of the repository at root that are clean, have every commit on a remote, went untouched
// for Idle and hold no cwd in cwds, then deletes local branches merged into Base that no worktree has checked out.
// With dry it only decides; with sizes it measures every worktree it removes or keeps for dirt or local commits.
func GC(root string, now time.Time, cwds []string, dry, sizes bool) (Swept, error) {
	s := Swept{Root: root}
	all, err := worktrees(root)
	if err != nil {
		return s, err
	}
	used := map[string]bool{"main": true, "master": true}
	for _, w := range all {
		if w.main {
			used[w.branch] = true
			continue
		}
		sw := Sweep{Path: w.path, Branch: w.branch}
		idle, local := now.Sub(touched(w.path)), 0
		status, err := look(w.path, "status", "--porcelain")
		if err == nil {
			var n string
			n, err = look(w.path, "rev-list", "--count", "HEAD", "--not", "--remotes")
			local, _ = strconv.Atoi(n)
		}
		switch {
		case w.locked:
			sw.Keep, sw.Why = KeepLocked, "locked or missing"
		case err != nil:
			sw.Keep, sw.Why = KeepFailing, err.Error()
		default:
			sw.Keep, sw.Why = Keep(status != "", local, idle, inside(w.path, cwds))
		}
		if sizes && (sw.Keep == "" || sw.Keep == KeepDirty || sw.Keep == KeepLocal) {
			sw.Size = Size(w.path)
		}
		if sw.Keep == "" && !dry {
			if _, err := git(root, "worktree", "remove", w.path); err != nil {
				sw.Keep, sw.Why = KeepFailing, err.Error()
				s.Errors = append(s.Errors, err.Error())
			}
		}
		if sw.Keep != "" {
			used[w.branch] = true
		}
		s.Trees = append(s.Trees, sw)
	}
	base := Base(root)
	if base == "" {
		return s, nil
	}
	merged, err := git(root, "branch", "--format=%(refname:short)", "--merged", base)
	if err != nil {
		return s, err
	}
	for _, b := range strings.Fields(merged) {
		if used[b] {
			continue
		}
		if !dry {
			if _, err := git(root, "branch", "-D", b); err != nil {
				s.Errors = append(s.Errors, err.Error())
				continue
			}
		}
		s.Branches = append(s.Branches, b)
	}
	return s, nil
}

// DropMerged deletes branch from the repository at root when it is merged into Base; ok reports a deletion.
func DropMerged(root, branch string) (bool, error) {
	base := Base(root)
	if base == "" || branch == "" {
		return false, nil
	}
	if _, err := git(root, "merge-base", "--is-ancestor", "refs/heads/"+branch, base); err != nil {
		return false, nil
	}
	if _, err := git(root, "branch", "-D", branch); err != nil {
		return false, err
	}
	return true, nil
}
