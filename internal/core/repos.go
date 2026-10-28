package core

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// RepoDepth is how many directories below a root ScanRepos looks for a repository.
const RepoDepth = 2

// RepoRoots are the directories wt-reap names in ~/.config/wt-reap/roots, one per line, or ~/dev without that file.
func RepoRoots() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var roots []string
	if f, err := os.Open(filepath.Join(home, ".config", "wt-reap", "roots")); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if line := strings.TrimSpace(sc.Text()); line != "" && !strings.HasPrefix(line, "#") {
				roots = append(roots, ExpandHome(line))
			}
		}
	}
	if len(roots) == 0 {
		roots = []string{filepath.Join(home, "dev")}
	}
	return roots
}

// ExpandHome replaces a leading ~ with the home directory.
func ExpandHome(p string) string {
	if p != "~" && !strings.HasPrefix(p, "~/") {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	return filepath.Join(home, p[1:])
}

// ScanRepos sends each main checkout found up to RepoDepth directories below roots, once, then closes found.
// A worktree's .git is a file, so worktrees collapse into their main checkout.
func ScanRepos(roots []string, found chan<- string) {
	defer close(found)
	seen := map[string]bool{}
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		if fi, err := os.Stat(filepath.Join(dir, ".git")); err == nil && fi.IsDir() {
			common, err := filepath.EvalSymlinks(filepath.Join(dir, ".git"))
			if err == nil && !seen[common] {
				seen[common] = true
				found <- filepath.Dir(common)
			}
		}
		if depth == RepoDepth {
			return
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(dir, e.Name())
			if fi, err := os.Stat(p); err == nil && fi.IsDir() {
				walk(p, depth+1)
			}
		}
	}
	for _, r := range roots {
		if r, err := filepath.EvalSymlinks(r); err == nil {
			walk(r, 0)
		}
	}
}

// MainCheckout is the main checkout of the repository dir lies in, or dir when it has none.
func MainCheckout(dir string) string {
	g, ok := readGitFiles(dir)
	if !ok {
		g = revParse(dir)
	}
	if filepath.Base(g.Common) == ".git" {
		return filepath.Dir(g.Common)
	}
	return filepath.Clean(dir)
}

// OrderRepos lists recent repositories first, most recently picked first, then the scanned ones by name.
func OrderRepos(recent, scanned []string) []string {
	rest := slices.Clone(scanned)
	slices.SortFunc(rest, func(a, b string) int {
		if c := strings.Compare(strings.ToLower(filepath.Base(a)), strings.ToLower(filepath.Base(b))); c != 0 {
			return c
		}
		return strings.Compare(a, b)
	})
	out := slices.Clone(recent)
	for _, r := range rest {
		if !slices.Contains(out, r) {
			out = append(out, r)
		}
	}
	return out
}

// repoScore ranks how well query names repo, by its directory or its origin's name: 0 is an exact name, then a name
// prefix, a name substring, a path substring and the query's letters in order in the name; -1 does not match.
func repoScore(repo, query string) int {
	best := nameScore(filepath.Base(repo), repo, query)
	if id := RepoIDOf(repo); id != "" && !filepath.IsAbs(id) {
		if s := nameScore(RepoLabel(id), id, query); s >= 0 && (best < 0 || s < best) {
			best = s
		}
	}
	return best
}

func nameScore(name, path, query string) int {
	q, name, path := strings.ToLower(query), strings.ToLower(name), strings.ToLower(path)
	switch {
	case name == q:
		return 0
	case strings.HasPrefix(name, q):
		return 1
	case strings.Contains(name, q):
		return 2
	case strings.Contains(path, q):
		return 3
	}
	i := 0
	for _, r := range name {
		if i < len(q) && rune(q[i]) == r {
			i++
		}
	}
	if i == len(q) {
		return 4
	}
	return -1
}

// MatchRepos keeps the repos query names, best match first, ties in their given order.
func MatchRepos(repos []string, query string) []string {
	if strings.TrimSpace(query) == "" {
		return repos
	}
	var out []string
	for _, r := range repos {
		if repoScore(r, query) >= 0 {
			out = append(out, r)
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return repoScore(a, query) - repoScore(b, query) })
	return out
}

// IsRepoPath reports whether ref is a path rather than a repository name.
func IsRepoPath(ref string) bool {
	return strings.HasPrefix(ref, "~") || strings.HasPrefix(ref, ".") || strings.ContainsRune(ref, filepath.Separator)
}

// Repos lists every repository the picker offers: recently picked ones first, then those under RepoRoots.
func (c *Core) Repos() ([]string, error) {
	recent, err := c.Store.RecentRepos()
	if err != nil {
		return nil, err
	}
	var roots []string
	if c.Roots != nil {
		roots = c.Roots()
	}
	ch := make(chan string)
	go ScanRepos(roots, ch)
	var scanned []string
	for r := range ch {
		scanned = append(scanned, r)
	}
	return OrderRepos(recent, scanned), nil
}

// ResolveRepo turns a repository name, or any part of it, into its main checkout; a path is taken as it is. Clones of
// one repository count as one, the most recently picked first.
func (c *Core) ResolveRepo(ref string) (string, error) {
	if IsRepoPath(ref) {
		return ExpandHome(ref), nil
	}
	repos, err := c.Repos()
	if err != nil {
		return "", err
	}
	matches := MatchRepos(repos, ref)
	if len(matches) == 0 {
		return "", fmt.Errorf("no repository matches %q", ref)
	}
	idOf := func(r string) string {
		if id := RepoIDOf(r); id != "" {
			return id
		}
		return r
	}
	best := repoScore(matches[0], ref)
	var tied []string
	ids := map[string]bool{}
	for _, m := range matches {
		if repoScore(m, ref) == best {
			tied = append(tied, m)
			ids[idOf(m)] = true
		}
	}
	if len(ids) > 1 {
		return "", fmt.Errorf("%q matches several repositories; name one by path:\n  %s", ref, strings.Join(tied, "\n  "))
	}
	for _, r := range repos {
		if ids[idOf(r)] {
			return r, nil
		}
	}
	return tied[0], nil
}

// StartNew launches n and remembers its repository so pickers offer it first.
func (c *Core) StartNew(n NewLaunch) (string, error) {
	if host, ok := c.RepoHost(n.Repo); ok {
		args := []string{"new", "--repo", MainCheckout(n.Repo)}
		if n.Harness == "codex" {
			args = append(args, "--codex")
		}
		if n.Slug != "" {
			args = append(args, "--slug", n.Slug)
		}
		if n.Profile != "" {
			args = append(args, "--profile", n.Profile)
		}
		out, err := c.RunOrRelay(host, append(args, "--", n.Prompt)...)
		return "on " + host + ": " + strings.TrimSpace(out), err
	}
	if c.Launches == nil {
		return "", errors.New("ym's launcher is unavailable")
	}
	st, err := c.Launches.Start(n)
	if err != nil {
		return st.Message, err
	}
	return st.Message, c.Store.PickRepo(MainCheckout(n.Repo), time.Now())
}

// PickRepo records that repo was just picked for a new session.
func (s *Store) PickRepo(repo string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO ym_repos (path, picked_at) VALUES (?, ?) ON CONFLICT(path) DO UPDATE SET picked_at = excluded.picked_at`, repo, at.UnixNano())
	return err
}

// RecentRepos lists picked repositories that still exist, most recently picked first.
func (s *Store) RecentRepos() ([]string, error) {
	rows, err := s.db.Query(`SELECT path FROM ym_repos ORDER BY picked_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

// KnownRepos are the main checkouts of repositories picked for a new session or holding a running session.
func (c *Core) KnownRepos() ([]string, error) {
	recent, err := c.Store.RecentRepos()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	add := func(dir string) {
		root := MainCheckout(dir)
		if fi, err := os.Stat(filepath.Join(root, ".git")); err == nil && fi.IsDir() && !seen[root] {
			seen[root] = true
			out = append(out, root)
		}
	}
	for _, r := range recent {
		add(r)
	}
	procs, err := c.Procs()
	if err != nil {
		return out, err
	}
	for _, owned := range c.owned(procs) {
		for _, p := range owned {
			if dir := proc.Cwd(p.PID); dir != "" && !c.foreign(dir) {
				add(dir)
			}
		}
	}
	return out, nil
}
