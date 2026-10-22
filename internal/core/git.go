package core

import (
	"fmt"
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
	// Default is the repository's default branch, "" when it cannot be told.
	Default string
	// Ahead counts the branch's commits past the default branch.
	Ahead int
}

// DefaultBranch reports whether a branch is the conventional default, main or master.
func DefaultBranch(b string) bool { return b == "main" || b == "master" }

// IsDefault reports whether b is the repository's default branch, falling back to main or master.
func (g GitInfo) IsDefault(b string) bool {
	if g.Default != "" {
		return b == g.Default
	}
	return DefaultBranch(b)
}

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

// ReadGit finds where dir sits from the checkout's .git files, asking git only when they are unusual.
func ReadGit(dir string) GitInfo {
	g, ok := readGitFiles(dir)
	if !ok {
		g = revParse(dir)
	}
	if g.Branch != "" && g.Branch != "HEAD" && !g.IsDefault(g.Branch) {
		g.Ahead = aheadOnce(dir, g)
	}
	return g
}

type aheadEntry struct {
	stamp string
	n     int
}

var (
	aheadMu    sync.Mutex
	aheadCache = map[string]aheadEntry{}
)

// aheadOnce counts dir's commits ahead, asking git again only after HEAD or a ref it counts against moves.
func aheadOnce(dir string, g GitInfo) int {
	stamp := refsStamp(g)
	aheadMu.Lock()
	e, ok := aheadCache[dir]
	aheadMu.Unlock()
	if ok && stamp != "" && e.stamp == stamp {
		return e.n
	}
	n := ahead(dir, g.Default)
	aheadMu.Lock()
	aheadCache[dir] = aheadEntry{stamp, n}
	aheadMu.Unlock()
	return n
}

// refsStamp is the size and time of every file holding HEAD or a ref ahead counts against, "" when the checkout's git dir is unusual.
func refsStamp(g GitInfo) string {
	gitDir := filepath.Join(g.Top, ".git")
	if b, err := os.ReadFile(gitDir); err == nil {
		line, ok := strings.CutPrefix(strings.TrimSpace(string(b)), "gitdir: ")
		if !ok {
			return ""
		}
		gitDir = line
		if !filepath.IsAbs(gitDir) {
			gitDir = filepath.Join(g.Top, gitDir)
		}
	}
	files := []string{filepath.Join(gitDir, "HEAD"), filepath.Join(g.Common, "packed-refs"), filepath.Join(g.Common, "refs", "heads", g.Branch)}
	for _, b := range []string{"main", "master", g.Default} {
		if b != "" {
			files = append(files, filepath.Join(g.Common, "refs", "heads", b), filepath.Join(g.Common, "refs", "remotes", "origin", b))
		}
	}
	files = append(files, filepath.Join(g.Common, "refs", "remotes", "origin", "HEAD"))
	var sb strings.Builder
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			fmt.Fprintf(&sb, "%s %d %d;", f, info.Size(), info.ModTime().UnixNano())
		} else {
			fmt.Fprintf(&sb, "%s -;", f)
		}
	}
	return sb.String()
}

// RepoName is the name of the repository dir is a checkout of, as its pull request links and ym_repo_authority name it: origin's
// repository name, or the directory's own name without an origin.
func RepoName(dir string) string {
	out, err := exec.Command("git", "-C", dir, "config", "--get", "remote.origin.url").Output()
	url := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(string(out)), "/"), ".git")
	if i := strings.LastIndexAny(url, "/:"); err == nil && i >= 0 && i < len(url)-1 {
		return strings.ToLower(url[i+1:])
	}
	return strings.ToLower(filepath.Base(dir))
}

func revParse(dir string) GitInfo {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return GitInfo{}
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 3 {
		return GitInfo{}
	}
	g := GitInfo{Top: lines[0], Common: filepath.Clean(lines[1]), Branch: lines[2]}
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		g.Default = strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/")
	}
	return g
}

// originHead reads the default branch from the symref a clone leaves at origin/HEAD.
func originHead(common string) string {
	b, err := os.ReadFile(filepath.Join(common, "refs", "remotes", "origin", "HEAD"))
	if err != nil {
		return ""
	}
	name, _ := strings.CutPrefix(strings.TrimSpace(string(b)), "ref: refs/remotes/origin/")
	return name
}

// readGitFiles walks up to the nearest .git and reads its HEAD and commondir.
func readGitFiles(dir string) (GitInfo, bool) {
	dir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return GitInfo{}, true
	}
	for d := dir; ; d = filepath.Dir(d) {
		dot := filepath.Join(d, ".git")
		info, err := os.Lstat(dot)
		if err == nil {
			gitDir := dot
			if !info.IsDir() {
				b, err := os.ReadFile(dot)
				line := strings.TrimSpace(string(b))
				if err != nil || !strings.HasPrefix(line, "gitdir: ") {
					return GitInfo{}, false
				}
				gitDir = strings.TrimPrefix(line, "gitdir: ")
				if !filepath.IsAbs(gitDir) {
					gitDir = filepath.Join(d, gitDir)
				}
			}
			common := gitDir
			if b, err := os.ReadFile(filepath.Join(gitDir, "commondir")); err == nil {
				common = strings.TrimSpace(string(b))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
			}
			head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return GitInfo{}, false
			}
			branch := "HEAD"
			if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); ok {
				branch = ref
			}
			common = filepath.Clean(common)
			return GitInfo{Top: d, Common: common, Branch: branch, Default: originHead(common)}, true
		}
		if p := filepath.Dir(d); p == d {
			return GitInfo{}, true
		}
	}
}

// ahead counts HEAD's commits on none of the branches work merges into.
func ahead(dir, def string) int {
	not := []string{"origin/HEAD", "origin/main", "origin/master", "main", "master"}
	if def != "" {
		not = append(not, "origin/"+def, def)
	}
	out, err := exec.Command("git", append([]string{"-C", dir, "rev-list", "--ignore-missing", "--count", "HEAD", "--not"}, not...)...).Output()
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(out)))
	return n
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
