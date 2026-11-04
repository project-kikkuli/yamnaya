package launch

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Recovery is the work a kill took out of a worktree that no remote or default branch holds, kept as a git bundle.
type Recovery struct {
	Bundle string `json:"bundle,omitempty"`
	// Work is the bundled commit on top of HEAD holding the uncommitted and untracked changes, "" when there were none.
	Work    string `json:"work,omitempty"`
	Commits int    `json:"commits,omitempty"`
	Files   int    `json:"files,omitempty"`
	// Origin is the repository's origin URL, so the work can be rebuilt once the repository is gone.
	Origin string `json:"origin,omitempty"`
}

// Toll is what removing a worktree would take with it: commits no remote or default branch holds, and changed or untracked files.
type Toll struct{ Commits, Files int }

func (t Toll) Add(o Toll) Toll { return Toll{t.Commits + o.Commits, t.Files + o.Files} }

// Weigh reads dir's Toll.
func Weigh(dir string) (Toll, error) {
	status, err := git(dir, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return Toll{}, err
	}
	var t Toll
	if status != "" {
		t.Files = len(strings.Split(status, "\n"))
	}
	n, err := git(dir, append([]string{"rev-list", "--count", "HEAD", "--not", "--remotes"}, defaults(dir)...)...)
	if err != nil {
		return t, err
	}
	t.Commits, _ = strconv.Atoi(n)
	return t, nil
}

func defaults(dir string) []string {
	var out []string
	for _, ref := range []string{"refs/heads/main", "refs/heads/master"} {
		if _, err := git(dir, "rev-parse", "--verify", "--quiet", ref); err == nil {
			out = append(out, ref)
		}
	}
	return out
}

func recoveryRef(key string) string { return "refs/ym-recovery/" + key }

// Stash bundles into dir the work of worktree path that Weigh counts, so Recover can put it back once the worktree is gone.
func Stash(path, key, dir string) (Recovery, error) {
	t, err := Weigh(path)
	if err != nil || t == (Toll{}) {
		return Recovery{}, err
	}
	r := Recovery{Commits: t.Commits, Files: t.Files, Bundle: filepath.Join(dir, key+".bundle")}
	r.Origin, _ = git(path, "remote", "get-url", "origin")
	tip := "HEAD"
	if t.Files > 0 {
		tree, err := worktreeTree(path, key, dir, false)
		if err != nil {
			return r, err
		}
		if r.Work, err = gitEnv(path, []string{"GIT_AUTHOR_NAME=ym", "GIT_AUTHOR_EMAIL=ym@localhost", "GIT_COMMITTER_NAME=ym", "GIT_COMMITTER_EMAIL=ym@localhost"},
			nil, "commit-tree", tree, "-p", "HEAD", "-m", "uncommitted work ym kill kept"); err != nil {
			return r, err
		}
		tip = r.Work
	}
	ref := recoveryRef(key)
	if _, err := git(path, "update-ref", ref, tip); err != nil {
		return r, err
	}
	defer git(path, "update-ref", "-d", ref)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return r, err
	}
	_, err = git(path, append([]string{"bundle", "create", "--quiet", r.Bundle, ref, "--not", "--remotes"}, defaults(path)...)...)
	return r, err
}

// worktreeTree writes the tree of everything in the worktree at path that git add -A takes, leaving its index alone. With cached it
// starts from a copy of that index, whose file times spare hashing every tracked file again; a path force-added past an ignore
// rule then counts too, so only a comparison that errs toward a difference may use it.
func worktreeTree(path, key, dir string, cached bool) (string, error) {
	index := filepath.Join(dir, key+".index")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	steps := [][]string{{"read-tree", "HEAD"}, {"add", "-A"}}
	if real, err := git(path, "rev-parse", "--path-format=absolute", "--git-path", "index"); cached && err == nil {
		if raw, err := os.ReadFile(real); err == nil && os.WriteFile(index, raw, 0o600) == nil {
			steps = steps[1:]
		}
	}
	for _, args := range steps {
		if _, err := gitEnv(path, env, nil, args...); err != nil {
			return "", err
		}
	}
	return gitEnv(path, env, nil, "write-tree")
}

// What a checkout at a recorded worktree's path says about the bundle of that worktree's work.
const (
	// WorkHere is the same worktree holding every commit and every file of the bundle: it has nothing to put back.
	WorkHere = iota
	// WorkMissing is the same worktree, clean at the recorded commit: the bundle's files apply to it.
	WorkMissing
	// WorkChanged is the same worktree differing from the bundle, so neither dropping it nor applying it is safe.
	WorkChanged
	// WorkElsewhere is some other checkout.
	WorkElsewhere
)

// Standing compares the checkout at w's path with this bundle of w, going only by what the bundle itself holds. It is w while it
// shares w's repository and is on w's branch, or with no branch while the bundle's commits are behind it, so a HEAD that moved on
// stays w. Only its commits being in that HEAD and its tree matching prove the bundle holds nothing more, because a worktree added
// again by hand, amended, rebased or reset on the same branch is w by every other measure.
func (r Recovery) Standing(w Worktree, key string) int {
	common, err := git(w.Path, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !samePath(common, w.GitDir) {
		return WorkElsewhere
	}
	branch, _ := git(w.Path, "symbolic-ref", "--short", "-q", "HEAD")
	if branch != w.Branch {
		return WorkElsewhere
	}
	base := recoveryRef(key)
	if r.Work != "" {
		base += "^"
	}
	fetched := r.Fetch(w.GitDir, key) == nil
	head := Head(w.Path)
	behind := fetched && local("--git-dir="+w.GitDir, "merge-base", "--is-ancestor", base, head) == nil
	if branch == "" && !behind {
		return WorkElsewhere
	}
	if !behind {
		return WorkChanged
	}
	if r.Work == "" {
		return WorkHere
	}
	want, err := git(w.Path, "rev-parse", recoveryRef(key)+"^{tree}")
	if err != nil {
		return WorkChanged
	}
	if tree, err := worktreeTree(w.Path, key, filepath.Dir(r.Bundle), true); err == nil && tree == want {
		return WorkHere
	}
	status, err := look(w.Path, "status", "--porcelain", "--untracked-files=normal")
	if at, atErr := git(w.Path, "rev-parse", base); err == nil && status == "" && atErr == nil && at == head {
		return WorkMissing
	}
	return WorkChanged
}

func samePath(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// Fetch brings the bundled commits of key into the repository at gitDir, so its worktree can be recreated at them.
func (r Recovery) Fetch(gitDir, key string) error {
	if r.Bundle == "" {
		return nil
	}
	if _, err := os.Stat(r.Bundle); err != nil {
		return fmt.Errorf("recovery bundle %s: %w", r.Bundle, err)
	}
	ref := recoveryRef(key)
	out, err := gitCommand("--git-dir="+gitDir, "fetch", "--quiet", r.Bundle, "+"+ref+":"+ref).CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch %s: %w: %s", r.Bundle, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Rebuild places the work into path, an empty directory left where a worktree whose repository is gone was: a clone of the
// origin, or a new repository when there is none, given the bundle's commits and checked out at head on branch, the
// uncommitted changes on top. When it cannot, it says where the bundle is and how to apply it by hand.
func (r Recovery) Rebuild(path, key, head, branch string) error {
	ref := recoveryRef(key)
	steps := [][]string{{"init", "-q", path}}
	if r.Origin != "" {
		steps = [][]string{{"clone", "-q", "--no-checkout", r.Origin, path}}
	}
	at := head
	if at == "" {
		at = ref
	}
	steps = append(steps, []string{"-C", path, "fetch", "-q", r.Bundle, "+" + ref + ":" + ref}, []string{"-C", path, "checkout", "-q", "-B", branch, at})
	for _, args := range steps {
		if out, err := gitCommand(args...).CombinedOutput(); err != nil {
			return fmt.Errorf("could not rebuild %s from %s (git %s: %s); apply it by hand: git clone %s %s && git -C %s fetch %s %s:%s && git -C %s checkout -B %s %s, then git -C %s diff %s %s | git -C %s apply",
				path, r.Bundle, strings.Join(args, " "), strings.TrimSpace(string(out)), originOr(r.Origin), path, path, r.Bundle, ref, ref, path, branch, at, path, at, ref, path)
		}
	}
	return r.Apply(path, key)
}

func originOr(origin string) string {
	if origin == "" {
		return "<origin>"
	}
	return origin
}

// Apply puts the uncommitted changes back into worktree path, then drops the bundle and its ref.
func (r Recovery) Apply(path, key string) error {
	if r.Work != "" {
		diff, err := gitRaw(path, nil, nil, "diff", "--binary", "HEAD", r.Work)
		if err != nil {
			return err
		}
		if diff != "" {
			if _, err := gitEnv(path, nil, []byte(diff), "apply", "--whitespace=nowarn"); err != nil {
				return err
			}
		}
	}
	return r.Drop(path, key)
}

// Drop deletes the bundle and its ref once what they saved is back in the worktree at path.
func (r Recovery) Drop(path, key string) error {
	git(path, "update-ref", "-d", recoveryRef(key))
	if r.Bundle != "" {
		if err := os.Remove(r.Bundle); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func gitEnv(dir string, env []string, stdin []byte, args ...string) (string, error) {
	out, err := gitRaw(dir, env, stdin, args...)
	return strings.TrimRight(out, "\n"), err
}

// gitRaw is gitEnv keeping every byte git wrote: a binary patch ends in a blank line it cannot do without.
func gitRaw(dir string, env []string, stdin []byte, args ...string) (string, error) {
	cmd := gitCommand(append([]string{"-C", dir}, args...)...)
	cmd.Env = append(cmd.Env, env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errOut.String()))
	}
	return out.String(), nil
}
