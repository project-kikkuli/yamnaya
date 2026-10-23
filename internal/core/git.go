package core

import (
	"fmt"
	"os"
	"os/exec"
	"path"
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
	// Origin is the repository's origin remote as host/owner/name, the same for every clone on every host; "" without one.
	Origin string
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

// Name is the repository's name: origin's, else its main checkout's directory, or the bare repo's.
func (g GitInfo) Name() string {
	if g.Origin != "" {
		return path.Base(g.Origin)
	}
	return RepoLabel(g.Common)
}

// ID is the repository's identity: its origin, else its git common dir.
func (g GitInfo) ID() string {
	if g.Origin != "" {
		return g.Origin
	}
	return g.Common
}

// RepoLabel names the repository a RepoID or git common dir stands for.
func RepoLabel(id string) string {
	if id == "" {
		return ""
	}
	if path.Base(id) == ".git" {
		return path.Base(path.Dir(id))
	}
	return strings.TrimSuffix(path.Base(id), ".git")
}

// OriginKey normalizes a remote URL to host/owner/name in lower case, so ssh and https forms of one repository agree;
// "" for a local path.
func OriginKey(url string) string {
	u := strings.TrimSpace(url)
	var host, rest string
	if scheme, after, ok := strings.Cut(u, "://"); ok {
		if scheme == "file" {
			return ""
		}
		host, rest, _ = strings.Cut(after, "/")
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
		if h, _, ok := strings.Cut(host, ":"); ok {
			host = h
		}
	} else if h, r, ok := strings.Cut(u, ":"); ok && !strings.Contains(h, "/") {
		host, rest = h, r
		if i := strings.LastIndex(host, "@"); i >= 0 {
			host = host[i+1:]
		}
	} else {
		return ""
	}
	rest = strings.TrimSuffix(strings.Trim(rest, "/"), ".git")
	if host == "" || rest == "" {
		return ""
	}
	return strings.ToLower(host + "/" + rest)
}

// originOf reads origin's URL from the repository's config file.
func originOf(common string) string {
	b, err := readStable(filepath.Join(common, "config"))
	if err != nil {
		return ""
	}
	inOrigin := false
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			inOrigin = l == `[remote "origin"]`
			continue
		}
		if k, v, ok := strings.Cut(l, "="); inOrigin && ok && strings.TrimSpace(k) == "url" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

var repoIDs sync.Map

// RepoID is the identity of the repository with git common dir common: its origin, else common itself.
func RepoID(common string) string {
	if common == "" {
		return ""
	}
	if id, ok := repoIDs.Load(common); ok {
		return id.(string)
	}
	id := OriginKey(originOf(common))
	if id == "" {
		id = common
	}
	repoIDs.Store(common, id)
	return id
}

var dirIDs sync.Map

// RepoIDOf is the identity of the repository dir lies in, "" outside one.
func RepoIDOf(dir string) string {
	if id, ok := dirIDs.Load(dir); ok {
		return id.(string)
	}
	g, ok := readGitFiles(dir)
	if !ok {
		g = revParse(dir)
	}
	dirIDs.Store(dir, g.ID())
	return g.ID()
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
	g, ok := readGitFiles(dir)
	if !ok {
		g = revParse(dir)
	}
	if g.Origin != "" {
		return g.Name()
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
	g.Origin = OriginKey(originOf(g.Common))
	if out, err := exec.Command("git", "-C", dir, "symbolic-ref", "--short", "refs/remotes/origin/HEAD").Output(); err == nil {
		g.Default = strings.TrimPrefix(strings.TrimSpace(string(out)), "origin/")
	}
	return g
}

// originHead reads the default branch from the symref a clone leaves at origin/HEAD.
func originHead(common string) string {
	b, err := readStable(filepath.Join(common, "refs", "remotes", "origin", "HEAD"))
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
				b, err := readStable(dot)
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
			if b, err := readStable(filepath.Join(gitDir, "commondir")); err == nil {
				common = strings.TrimSpace(string(b))
				if !filepath.IsAbs(common) {
					common = filepath.Join(gitDir, common)
				}
			}
			head, err := readStable(filepath.Join(gitDir, "HEAD"))
			if err != nil {
				return GitInfo{}, false
			}
			branch := "HEAD"
			if ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: refs/heads/"); ok {
				branch = ref
			}
			common = filepath.Clean(common)
			return GitInfo{Top: d, Common: common, Origin: OriginKey(originOf(common)), Branch: branch, Default: originHead(common)}, true
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

type stableFile struct {
	info os.FileInfo
	data []byte
}

var (
	stableMu    sync.Mutex
	stableFiles = map[string]stableFile{}
)

// readStable reads a small git file, opening it again only after its size, time or identity changes: a stat costs far less than
// an open on a machine that scans every file opened.
func readStable(path string) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		stableMu.Lock()
		delete(stableFiles, path)
		stableMu.Unlock()
		return nil, err
	}
	stableMu.Lock()
	f, ok := stableFiles[path]
	stableMu.Unlock()
	if ok && f.info.Size() == info.Size() && f.info.ModTime().Equal(info.ModTime()) && os.SameFile(f.info, info) {
		return f.data, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	stableMu.Lock()
	stableFiles[path] = stableFile{info, data}
	stableMu.Unlock()
	return data, nil
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

// gitFresh is dir's place when read within gitTTL.
func (c *Core) gitFresh(dir string, now time.Time) (GitInfo, bool) {
	if dir == "" || c.Git == nil {
		return GitInfo{}, true
	}
	c.gc.mu.Lock()
	e, ok := c.gc.info[dir]
	c.gc.mu.Unlock()
	return e.GitInfo, ok && now.Sub(e.at) < gitTTL
}

func (c *Core) gitFor(dir string, now time.Time) GitInfo {
	if g, ok := c.gitFresh(dir, now); ok {
		return g
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
