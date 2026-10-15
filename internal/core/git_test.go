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

func TestReadGitResolvesTheDefaultBranch(t *testing.T) {
	cases := []struct {
		name, branch, originHead string
		wantDefault              string
		wantAhead                int
	}{
		{"develop default, checked out", "develop", "develop", "develop", 0},
		{"develop default, feature ahead of it", "feat", "develop", "develop", 1},
		{"main default", "feat", "main", "main", 1},
		{"no origin/HEAD falls back to main", "feat", "", "", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			git := func(args ...string) {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
				cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
			}
			base := "develop"
			if c.originHead == "main" || c.originHead == "" {
				base = "main"
			}
			git("init", "-q", "-b", base)
			git("commit", "-q", "--allow-empty", "-m", "one")
			if c.originHead != "" {
				git("update-ref", "refs/remotes/origin/"+c.originHead, "HEAD")
				git("symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/"+c.originHead)
			}
			if c.branch != base {
				git("checkout", "-q", "-b", c.branch)
				git("commit", "-q", "--allow-empty", "-m", "two")
			}
			got := ReadGit(dir)
			if got.Default != c.wantDefault || got.Ahead != c.wantAhead {
				t.Fatalf("ReadGit = default %q ahead %d, want %q %d", got.Default, got.Ahead, c.wantDefault, c.wantAhead)
			}
			if rp := revParse(dir); rp.Default != c.wantDefault {
				t.Fatalf("revParse default %q, want %q", rp.Default, c.wantDefault)
			}
			if wantIs := c.branch == base; got.IsDefault(c.branch) != wantIs {
				t.Fatalf("IsDefault(%q) = %v", c.branch, !wantIs)
			}
		})
	}
}
