package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GitInfo places a directory in its repository. Common is the git common dir, the same for every worktree of a repo.
type GitInfo struct {
	Top    string
	Common string
	Branch string
	// Ahead counts the branch's commits past the default branch.
	Ahead int
}

// DefaultBranch reports whether a branch is the one work merges into.
func DefaultBranch(b string) bool { return b == "main" || b == "master" }

// Name is the repository's name: its main checkout's directory, or the bare repo's.
func (g GitInfo) Name() string {
	if g.Common == "" {
		return ""
	}
	if filepath.Base(g.Common) == ".git" {
		return filepath.Base(filepath.Dir(g.Common))
	}
	return strings.TrimSuffix(filepath.Base(g.Common), ".git")
}

// ReadGit asks git where dir sits.
func ReadGit(dir string) GitInfo {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return GitInfo{}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return GitInfo{}
	}
	g := GitInfo{Top: lines[0], Common: filepath.Clean(lines[1]), Branch: lines[2]}
	if g.Branch != "HEAD" && !DefaultBranch(g.Branch) {
		g.Ahead = ahead(dir)
	}
	return g
}

func ahead(dir string) int {
	for _, base := range []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"} {
		out, err := exec.Command("git", "-C", dir, "rev-list", "--count", base+"..HEAD").Output()
		if err == nil {
			n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
			return n
		}
	}
	return 0
}

const gitTTL = 30 * time.Second

type gitCache struct {
	mu   sync.Mutex
	info map[string]gitEntry
	tops map[string]string
}

type gitEntry struct {
	GitInfo
	at time.Time
}

func (c *Core) gitFor(dir string, now time.Time) GitInfo {
	if dir == "" || c.Git == nil {
		return GitInfo{}
	}
	c.gc.mu.Lock()
	e, ok := c.gc.info[dir]
	c.gc.mu.Unlock()
	if ok && now.Sub(e.at) < gitTTL {
		return e.GitInfo
	}
	g := c.Git(dir)
	c.gc.mu.Lock()
	if c.gc.info == nil {
		c.gc.info = map[string]gitEntry{}
	}
	c.gc.info[dir] = gitEntry{g, now}
	c.gc.mu.Unlock()
	return g
}

// worktreeOf is the checkout holding path, found by walking up to the nearest .git entry.
func (c *Core) worktreeOf(path string) string {
	dir := filepath.Dir(path)
	c.gc.mu.Lock()
	defer c.gc.mu.Unlock()
	if c.gc.tops == nil {
		c.gc.tops = map[string]string{}
	}
	var walked []string
	top := ""
	for d := dir; ; d = filepath.Dir(d) {
		if t, ok := c.gc.tops[d]; ok {
			top = t
			break
		}
		walked = append(walked, d)
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil {
			top = d
			break
		}
		if p := filepath.Dir(d); p == d {
			break
		}
	}
	for _, d := range walked {
		c.gc.tops[d] = top
	}
	return top
}

// worktree is the checkout most of the member's written files sit in.
func (c *Core) worktree(files []string) string {
	count := map[string]int{}
	best := ""
	for _, f := range files {
		t := c.worktreeOf(f)
		if t == "" {
			continue
		}
		count[t]++
		if count[t] > count[best] {
			best = t
		}
	}
	return best
}
