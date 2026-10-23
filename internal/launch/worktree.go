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
	out, err := gitCommand(append([]string{"-C", dir}, args...)...).CombinedOutput()
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

// Fresh fetches the remote default branch of the repository at root and returns it, such as origin/main.
func Fresh(root string) (string, error) {
	base := Base(root)
	if base == "" {
		return "", fmt.Errorf("%s has no origin default branch to start a worktree from", root)
	}
	if _, err := git(root, "fetch", "--quiet", "origin", strings.TrimPrefix(base, "origin/")); err != nil {
		return "", fmt.Errorf("fetch %s to start a worktree from: %w", base, err)
	}
	return base, nil
}

// Create adds worktree slug to the repository dir belongs to with wt, on a new branch slug from the freshly fetched
// remote default branch, whatever the main checkout has checked out, numbering slug past any path held reports a record keeps.
func Create(dir, slug string, held func(path string) bool) (Worktree, error) {
	root, err := MainRoot(dir)
	if err != nil {
		return Worktree{}, err
	}
	base, err := Fresh(root)
	if err != nil {
		return Worktree{}, err
	}
	remote, err := remoteHeads(root)
	if err != nil {
		return Worktree{}, err
	}
	slug = Free(root, slug, func(p string) bool {
		_, err := os.Stat(p)
		return err == nil || held(p)
	}, func(b string) bool {
		_, err := git(root, "rev-parse", "--verify", "--quiet", "refs/heads/"+b)
		return err == nil || remote[b]
	})
	w := Worktree{Path: WorktreePath(root, slug), Branch: slug, GitDir: filepath.Join(root, ".git")}
	bin := os.Getenv("YAMNAYA_WT_BIN")
	if bin == "" {
		bin, _ = exec.LookPath("wt")
	}
	if bin == "" {
		if _, err := git(root, "worktree", "add", "-b", slug, w.Path, base); err != nil {
			return Worktree{}, err
		}
		return w, nil
	}
	cmd := exec.Command(bin, "add", slug, "--from="+base)
	cmd.Dir = root
	noAutoMaintenance(cmd)
	if out, err := cmd.CombinedOutput(); err != nil {
		return Worktree{}, fmt.Errorf("wt add %s in %s: %w: %s", slug, root, err, strings.TrimSpace(string(out)))
	}
	return w, nil
}

// remoteHeads are the branches origin has now, so a new branch never reuses the name of another clone's or a merged pull request's.
func remoteHeads(root string) (map[string]bool, error) {
	out, err := git(root, "ls-remote", "--heads", "origin")
	if err != nil {
		return nil, err
	}
	heads := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		if _, ref, ok := strings.Cut(line, "\t"); ok {
			heads[strings.TrimPrefix(ref, "refs/heads/")] = true
		}
	}
	return heads, nil
}

// RepoGone and DirGone are what a revive says when a worktree came back as an empty directory holding only the conversation.
const (
	RepoGone = "repo gone: conversation only, no code"
	DirGone  = "directory gone: conversation only, no files"
)

// Restore recreates a removed worktree from its repository, or from clones, git dirs of other clones of it, when the
// repository is gone. It uses the branch, else head, else the branch on origin, else the freshly fetched default branch.
// With no repository left it makes an empty directory at the path, so the conversation still resumes there, and says so in lost.
func Restore(w Worktree, head string, clones []string) (back Worktree, lost string, err error) {
	if _, err := os.Stat(w.Path); err == nil {
		return w, "", nil
	}
	if w.GitDir == "" {
		return w, DirGone, os.MkdirAll(w.Path, 0o755)
	}
	for _, gd := range append([]string{w.GitDir}, clones...) {
		if _, err := os.Stat(gd); err == nil {
			back = w
			back.GitDir = gd
			return back, "", recreate(back, head)
		}
	}
	return w, RepoGone, os.MkdirAll(w.Path, 0o755)
}

func recreate(w Worktree, head string) error {
	gd := "--git-dir=" + w.GitDir
	has := func(args ...string) bool { return gitCommand(append([]string{gd}, args...)...).Run() == nil }
	gitCommand(gd, "worktree", "prune").Run()
	headOK := head != "" && has("cat-file", "-e", head+"^{commit}")
	var add []string
	switch {
	case w.Branch == "" && headOK:
		add = []string{"--detach", w.Path, head}
	case w.Branch == "":
		return fmt.Errorf("%s is gone and neither its branch nor its commit was recorded to recreate it", w.Path)
	case has("show-ref", "--verify", "--quiet", "refs/heads/"+w.Branch):
		add = []string{w.Path, w.Branch}
	case headOK:
		add = []string{"-b", w.Branch, w.Path, head}
	case has("fetch", "--quiet", "origin", "+refs/heads/"+w.Branch+":refs/remotes/origin/"+w.Branch):
		add = []string{"-b", w.Branch, w.Path, "refs/remotes/origin/" + w.Branch}
	default:
		fresh, err := Fresh(filepath.Dir(w.GitDir))
		if err != nil {
			return fmt.Errorf("%s and its branch %s are gone with no recorded commit to recreate them from: %w", w.Path, w.Branch, err)
		}
		add = []string{"-b", w.Branch, w.Path, fresh}
	}
	if out, err := gitCommand(append([]string{gd, "worktree", "add"}, add...)...).CombinedOutput(); err != nil {
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
