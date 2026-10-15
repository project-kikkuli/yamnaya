package core

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestReadGitAgreesWithGitAcrossCheckouts(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo := filepath.Join(root, "repo")
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	os.MkdirAll(filepath.Join(repo, "sub"), 0o755)
	git(repo, "init", "-q", "-b", "main")
	git(repo, "commit", "-q", "--allow-empty", "-m", "one")
	git(repo, "worktree", "add", "-q", "-b", "feat", filepath.Join(root, "wt"))
	git(filepath.Join(root, "wt"), "commit", "-q", "--allow-empty", "-m", "two")
	git(filepath.Join(root, "wt"), "commit", "-q", "--allow-empty", "-m", "three")
	git(repo, "worktree", "add", "-q", "--detach", filepath.Join(root, "detached"))
	os.MkdirAll(filepath.Join(root, "plain"), 0o755)
	for _, c := range []struct {
		dir   string
		ahead int
	}{
		{repo, 0},
		{filepath.Join(repo, "sub"), 0},
		{filepath.Join(root, "wt"), 2},
		{filepath.Join(root, "detached"), 0},
		{filepath.Join(root, "plain"), 0},
		{filepath.Join(root, "gone"), 0},
	} {
		t.Run(filepath.Base(c.dir), func(t *testing.T) {
			got := ReadGit(c.dir)
			want := revParse(c.dir)
			want.Ahead = c.ahead
			if got != want {
				t.Fatalf("ReadGit = %+v, want %+v", got, want)
			}
		})
	}
}
