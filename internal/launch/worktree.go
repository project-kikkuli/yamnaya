// Package launch creates worktrees for new agent sessions, records them, and decides when they can be landed.
package launch

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

// MainRoot is the main checkout of the repository dir belongs to, even when dir is one of its worktrees.
func MainRoot(dir string) (string, error) {
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return "", fmt.Errorf("%s is not in a git repository: %w", dir, err)
	}
	if filepath.Base(common) != ".git" {
		return "", fmt.Errorf("%s has no main checkout (git dir %s)", dir, common)
	}
	return filepath.Dir(common), nil
}

// WorktreePath is where wt puts worktree slug of the repository at root: a sibling named <repo>-wt-<slug>.
func WorktreePath(root, slug string) string {
	return filepath.Join(filepath.Dir(root), filepath.Base(root)+"-wt-"+slug)
}

// Free picks slug, or slug-2, slug-3 and so on, so that neither the worktree directory nor the branch exists yet.
func Free(root, slug string, exists func(path string) bool, branch func(name string) bool) string {
	for n := 1; ; n++ {
		s := slug
		if n > 1 {
			s += "-" + strconv.Itoa(n)
		}
		if !exists(WorktreePath(root, s)) && !branch(s) {
			return s
		}
	}
}

// Worktree is a git worktree an agent runs in.
type Worktree struct {
	Path   string
	Branch string
	GitDir string
}

// Create adds worktree slug to the repository dir belongs to with wt, on a new branch slug from the default branch.
func Create(dir, slug string) (Worktree, error) {
	root, err := MainRoot(dir)
	if err != nil {
		return Worktree{}, err
	}
	slug = Free(root, slug, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil
	}, func(b string) bool {
		_, err := git(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b)
		return err == nil
	})
	w := Worktree{Path: WorktreePath(root, slug), Branch: slug, GitDir: filepath.Join(root, ".git")}
	bin := os.Getenv("YAMNAYA_WT_BIN")
	if bin == "" {
		bin, _ = exec.LookPath("wt")
	}
	if bin == "" {
		if _, err := git(root, "worktree", "add", "-b", slug, w.Path); err != nil {
			return Worktree{}, err
		}
		return w, nil
	}
	cmd := exec.Command(bin, "add", slug)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return Worktree{}, fmt.Errorf("wt add %s in %s: %w: %s", slug, root, err, strings.TrimSpace(string(out)))
	}
	return w, nil
}

// Restore recreates a removed worktree from its repository and branch, recreating the branch at head when that is gone too,
// or at the default branch when head is gone as well.
func Restore(w Worktree, head string) error {
	if _, err := os.Stat(w.Path); err == nil {
		return nil
	}
	if w.GitDir == "" || w.Branch == "" {
		return fmt.Errorf("%s is gone and no git branch was recorded to recreate it", w.Path)
	}
	gd := "--git-dir=" + w.GitDir
	exec.Command("git", gd, "worktree", "prune").Run()
	add := []string{gd, "worktree", "add", w.Path, w.Branch}
	if exec.Command("git", gd, "show-ref", "--verify", "--quiet", "refs/heads/"+w.Branch).Run() != nil {
		if head == "" || exec.Command("git", gd, "cat-file", "-e", head+"^{commit}").Run() != nil {
			head = Base(filepath.Dir(w.GitDir))
		}
		if head == "" {
			return fmt.Errorf("%s and its branch %s are gone and neither the recorded commit nor a default branch is left to recreate them", w.Path, w.Branch)
		}
		add = []string{gd, "worktree", "add", "-b", w.Branch, w.Path, head}
	}
	if out, err := exec.Command("git", add...).CombinedOutput(); err != nil {
		return fmt.Errorf("recreate %s from %s %s: %w: %s", w.Path, w.GitDir, w.Branch, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Head is the commit dir is on, or "".
func Head(dir string) string {
	out, _ := git(dir, "rev-parse", "HEAD")
	return out
}

// GitState is what landing a worktree depends on.
type GitState struct {
	Dirty    bool
	Unpushed int
	Merged   bool
	Into     string
	// Squashed is the merged pull request whose head holds every commit, or 0.
	Squashed int
}

// Landable reports whether removing the worktree loses nothing: it is clean and every commit is merged or pushed.
func (g GitState) Landable() (bool, string) {
	switch {
	case g.Dirty:
		return false, "has uncommitted or untracked changes"
	case g.Merged:
		return true, "merged into " + g.Into
	case g.Squashed > 0:
		return true, fmt.Sprintf("squash-merged in pull request #%d", g.Squashed)
	case g.Unpushed == 0:
		return true, "every commit is pushed"
	}
	return false, fmt.Sprintf("%d commits are neither merged into %s nor pushed", g.Unpushed, g.Into)
}

// State reads dir's GitState against the repository's default branch, main or master, local or on origin, asking q
// for a merged pull request when commits are neither merged nor pushed.
func State(dir string, q *Squash) (GitState, error) {
	status, err := git(dir, "status", "--porcelain")
	if err != nil {
		return GitState{}, err
	}
	g := GitState{Dirty: status != ""}
	for _, ref := range []string{"main", "master", "origin/main", "origin/master"} {
		if _, err := git(dir, "rev-parse", "--verify", "--quiet", ref); err != nil {
			continue
		}
		if g.Into == "" {
			g.Into = ref
		}
		if _, err := git(dir, "merge-base", "--is-ancestor", "HEAD", ref); err == nil {
			g.Merged, g.Into = true, ref
			break
		}
	}
	count := []string{"rev-list", "--count", "HEAD", "--not", "--remotes"}
	if g.Into == "" {
		g.Into = "the default branch"
	} else {
		count = append(count, g.Into)
	}
	n, err := git(dir, count...)
	if err != nil {
		return GitState{}, err
	}
	g.Unpushed, _ = strconv.Atoi(n)
	if g.Dirty || g.Merged || g.Unpushed == 0 {
		return g, nil
	}
	root, err := MainRoot(dir)
	if err != nil {
		return g, err
	}
	branch, _ := git(dir, "symbolic-ref", "--short", "-q", "HEAD")
	g.Squashed, err = q.Merged(root, branch, Head(dir))
	return g, err
}

// Remove deletes the worktree, keeping its branch; git refuses when it is dirty.
func Remove(w Worktree) error {
	root := filepath.Dir(w.GitDir)
	_, err := git(root, "worktree", "remove", w.Path)
	return err
}
