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
	tip := "HEAD"
	if t.Files > 0 {
		index := filepath.Join(dir, key+".index")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return r, err
		}
		defer os.Remove(index)
		env := []string{"GIT_INDEX_FILE=" + index}
		for _, args := range [][]string{{"read-tree", "HEAD"}, {"add", "-A"}} {
			if _, err := gitEnv(path, env, nil, args...); err != nil {
				return r, err
			}
		}
		tree, err := gitEnv(path, env, nil, "write-tree")
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

// Apply puts the uncommitted changes back into worktree path, then drops the bundle and its ref.
func (r Recovery) Apply(path, key string) error {
	if r.Work != "" {
		diff, err := gitEnv(path, nil, nil, "diff", "--binary", "HEAD", r.Work)
		if err != nil {
			return err
		}
		if diff != "" {
			if _, err := gitEnv(path, nil, []byte(diff+"\n"), "apply", "--whitespace=nowarn"); err != nil {
				return err
			}
		}
	}
	git(path, "update-ref", "-d", recoveryRef(key))
	if r.Bundle != "" {
		if err := os.Remove(r.Bundle); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func gitEnv(dir string, env []string, stdin []byte, args ...string) (string, error) {
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
	return strings.TrimRight(out.String(), "\n"), nil
}
