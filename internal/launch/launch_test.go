package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSlug(t *testing.T) {
	for _, c := range []struct{ prompt, want string }{
		{"say hi and stop", "say-hi-and-stop"},
		{"Fix the login bug in auth.go, then run tests", "fix-the-login-bug"},
		{"  ", "agent"},
		{"日本語", "agent"},
		{"refactor supercalifragilisticexpialidociouslylongword", "refactor"},
		{"supercalifragilisticexpialidociouslylongword", "supercalifragilisticexpialidocio"},
	} {
		if got := Slug(c.prompt); got != c.want {
			t.Errorf("Slug(%q) = %q, want %q", c.prompt, got, c.want)
		}
	}
}

func TestFreeSkipsTakenDirectoriesAndBranches(t *testing.T) {
	for _, c := range []struct {
		name           string
		dirs, branches []string
		want           string
	}{
		{"free", nil, nil, "x"},
		{"dir taken", []string{"/r/repo-wt-x"}, nil, "x-2"},
		{"branch taken", nil, []string{"x"}, "x-2"},
		{"both and next", []string{"/r/repo-wt-x"}, []string{"x-2"}, "x-3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			in := func(list []string) func(string) bool {
				return func(s string) bool {
					for _, v := range list {
						if v == s {
							return true
						}
					}
					return false
				}
			}
			if got := Free("/r/repo", "x", in(c.dirs), in(c.branches)); got != c.want {
				t.Errorf("Free = %q, want %q", got, c.want)
			}
		})
	}
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v %s", strings.Join(args, " "), err, out)
	}
}

func TestLandOnlyWhenNothingIsLost(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, wt string)
		ok    bool
	}{
		{"untouched branch", func(t *testing.T, wt string) {}, true},
		{"untracked file", func(t *testing.T, wt string) {
			os.WriteFile(filepath.Join(wt, "new.txt"), []byte("x"), 0o644)
		}, false},
		{"unpushed commit", func(t *testing.T, wt string) {
			run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "work")
		}, false},
		{"pushed commit", func(t *testing.T, wt string) {
			run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "work")
			run(t, wt, "git", "push", "-q", "origin", "HEAD")
		}, true},
		{"merged commit", func(t *testing.T, wt string) {
			run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "work")
			run(t, filepath.Join(filepath.Dir(wt), "repo"), "git", "merge", "-q", "--ff-only", "feature")
		}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
			run(t, base, "git", "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
			run(t, repo, "git", "remote", "add", "origin", filepath.Join(base, "origin.git"))
			run(t, repo, "git", "push", "-q", "origin", "main")
			wt := WorktreePath(repo, "feature")
			run(t, repo, "git", "worktree", "add", "-q", "-b", "feature", wt)
			c.setup(t, wt)
			g, err := State(wt)
			if err != nil {
				t.Fatal(err)
			}
			if ok, why := g.Landable(); ok != c.ok {
				t.Errorf("Landable = %v (%s), want %v; state %+v", ok, why, c.ok, g)
			}
			root, err := MainRoot(wt)
			if err != nil || root != repo {
				t.Errorf("MainRoot = %q, %v; want %q", root, err, repo)
			}
		})
	}
}

func TestRestoreRecreatesWorktreeAndBranch(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	run(t, base, "git", "init", "-q", "-b", "main", "repo")
	repo := filepath.Join(base, "repo")
	run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
	w := Worktree{Path: WorktreePath(repo, "f"), Branch: "f", GitDir: filepath.Join(repo, ".git")}
	run(t, repo, "git", "worktree", "add", "-q", "-b", "f", w.Path)
	run(t, w.Path, "git", "commit", "-q", "--allow-empty", "-m", "work")
	head := Head(w.Path)
	os.RemoveAll(w.Path)
	run(t, repo, "git", "worktree", "prune")
	run(t, repo, "git", "branch", "-q", "-D", "f")
	if err := Restore(w, head); err != nil {
		t.Fatal(err)
	}
	if got := Head(w.Path); got != head {
		t.Errorf("restored at %q, want %q", got, head)
	}
}

func TestRecordsFindByKeyBranchOrSessionPrefix(t *testing.T) {
	r := Records{Dir: t.TempDir()}
	rec := Record{Key: "repo-wt-x", Harness: "claude", Session: "0123456789abcdef", Worktree: Worktree{Branch: "x"}}
	if err := r.Save(rec); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"repo-wt-x", "x", "012345", "0123456789abcdef"} {
		if got, ok := r.Find(ref); !ok || got.Key != rec.Key {
			t.Errorf("Find(%q) = %+v, %v", ref, got, ok)
		}
	}
	for _, ref := range []string{"01234", "y"} {
		if _, ok := r.Find(ref); ok {
			t.Errorf("Find(%q) matched", ref)
		}
	}
}
