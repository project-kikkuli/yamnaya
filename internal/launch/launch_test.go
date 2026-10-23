package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
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
			g, err := State(wt, nil)
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

func TestRecordsFindPrefersALiveRecordOverALandedOne(t *testing.T) {
	for _, c := range []struct {
		name string
		recs []Record
		want string
	}{
		{"only landed", []Record{{Key: "a", Worktree: Worktree{Branch: "x"}, Landed: time.Now()}}, "a"},
		{"landed older, live newer", []Record{
			{Key: "a", Worktree: Worktree{Branch: "x"}, Started: time.Now().Add(-time.Hour), Landed: time.Now()},
			{Key: "b", Worktree: Worktree{Branch: "x"}, Started: time.Now()}}, "b"},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := Records{Dir: t.TempDir()}
			for _, rec := range c.recs {
				if err := r.Save(rec); err != nil {
					t.Fatal(err)
				}
			}
			if got, ok := r.Find("x"); !ok || got.Key != c.want {
				t.Fatalf("Find(x) = %q, %v; want %q", got.Key, ok, c.want)
			}
		})
	}
}

func TestCreateBranchesFromFreshRemoteBaseNotTheCheckout(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("no git")
	}
	for _, c := range []struct {
		name, wt string
		remote   bool
	}{
		{"git worktree", "", true},
		{"wt", "#!/bin/sh\n[ \"$1\" = add ] && case \"$3\" in --from=*) ;; *) exit 1;; esac\n" +
			"exec git worktree add -q -b \"$2\" \"../$(basename \"$PWD\")-wt-$2\" \"${3#--from=}\"\n", true},
		{"no remote", "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			run(t, base, "git", "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
			run(t, repo, "git", "checkout", "-q", "-b", "stale")
			run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "stale work")
			var tip string
			if c.remote {
				run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
				run(t, repo, "git", "remote", "add", "origin", filepath.Join(base, "origin.git"))
				run(t, repo, "git", "push", "-q", "origin", "main")
				run(t, repo, "git", "fetch", "-q", "origin")
				run(t, base, "git", "clone", "-q", "origin.git", "other")
				run(t, filepath.Join(base, "other"), "git", "commit", "-q", "--allow-empty", "-m", "ahead")
				run(t, filepath.Join(base, "other"), "git", "push", "-q", "origin", "main")
				tip = Head(filepath.Join(base, "other"))
			}
			bin := ""
			if c.wt != "" {
				bin = filepath.Join(base, "wt")
				os.WriteFile(bin, []byte(c.wt), 0o755)
			}
			t.Setenv("YAMNAYA_WT_BIN", bin)
			t.Setenv("PATH", filepath.Dir(gitBin)+":/bin:/usr/bin")
			checkout := Head(repo)
			w, err := Create(repo, "task", func(string) bool { return false })
			if !c.remote {
				if err == nil {
					t.Fatalf("Create without a remote = %+v, want an error", w)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if w.Path != WorktreePath(repo, "task") || w.Branch != "task" {
				t.Fatalf("worktree = %+v", w)
			}
			if got := Head(w.Path); got != tip {
				t.Errorf("worktree head %q, want remote tip %q", got, tip)
			}
			if got := Head(repo); got != checkout {
				t.Errorf("main checkout moved to %q from %q", got, checkout)
			}
		})
	}
}

func TestCreateSkipsBranchNamesOriginHasAndKeysRecordsKeep(t *testing.T) {
	for _, c := range []struct {
		name   string
		pushed []string
		held   []string
		want   string
	}{
		{"free on origin", nil, nil, "task"},
		{"another clone pushed it and its successor", []string{"task", "task-2"}, nil, "task-3"},
		{"landed records keep it and its successor", nil, []string{"task", "task-2"}, "task-3"},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
			run(t, base, "git", "clone", "-q", "origin.git", "other")
			other := filepath.Join(base, "other")
			run(t, other, "git", "commit", "-q", "--allow-empty", "-m", "root")
			run(t, other, "git", "push", "-q", "origin", "main")
			for _, b := range c.pushed {
				run(t, other, "git", "push", "-q", "origin", "main:"+b)
			}
			run(t, base, "git", "clone", "-q", "origin.git", "repo")
			t.Setenv("YAMNAYA_WT_BIN", "")
			t.Setenv("PATH", "/bin:/usr/bin:"+os.Getenv("PATH"))
			repo := filepath.Join(base, "repo")
			w, err := Create(repo, "task", func(p string) bool {
				return slices.ContainsFunc(c.held, func(s string) bool { return p == WorktreePath(repo, s) })
			})
			if err != nil {
				t.Fatal(err)
			}
			if w.Branch != c.want {
				t.Errorf("branch %q, want %q", w.Branch, c.want)
			}
		})
	}
}

func TestNormalizeSlug(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Mafchine Identity", "mafchine-identity"},
		{"MixedCase", "mixedcase"},
		{"feature/login", "feature-login"},
		{"a..b", "a-b"},
		{"foo.lock", "foo-lock"},
		{"-leading", "leading"},
		{"  --x--  ", "x"},
		{"café au lait", "caf-au-lait"},
		{"日本語", ""},
		{"", ""},
		{"~^:?*[ ", ""},
	} {
		got := NormalizeSlug(c.in)
		if got != c.want {
			t.Errorf("NormalizeSlug(%q) = %q, want %q", c.in, got, c.want)
		}
		if got != "" {
			if out, err := exec.Command("git", "check-ref-format", "--branch", got).CombinedOutput(); err != nil {
				t.Errorf("%q is not a valid branch: %s", got, out)
			}
		}
	}
}
