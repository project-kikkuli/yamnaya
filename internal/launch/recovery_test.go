package launch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStashThenRecoverBringsBackWorkNoRemoteHolds(t *testing.T) {
	for _, c := range []struct {
		name   string
		work   func(t *testing.T, wt string)
		toll   Toll
		bundle bool
	}{
		{"clean and pushed", func(t *testing.T, wt string) {}, Toll{}, false},
		{"unpushed commit", func(t *testing.T, wt string) {
			os.WriteFile(filepath.Join(wt, "done.txt"), []byte("committed"), 0o644)
			run(t, wt, "git", "add", "done.txt")
			run(t, wt, "git", "commit", "-q", "-m", "work")
		}, Toll{Commits: 1}, true},
		{"edited, deleted and untracked files on an unpushed commit", func(t *testing.T, wt string) {
			os.WriteFile(filepath.Join(wt, "done.txt"), []byte("committed"), 0o644)
			run(t, wt, "git", "add", "done.txt")
			run(t, wt, "git", "commit", "-q", "-m", "work")
			os.WriteFile(filepath.Join(wt, "done.txt"), []byte("edited"), 0o644)
			os.Remove(filepath.Join(wt, "base.txt"))
			os.WriteFile(filepath.Join(wt, "new.txt"), []byte("untracked"), 0o644)
		}, Toll{Commits: 1, Files: 3}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
			run(t, base, "git", "clone", "-q", filepath.Join(base, "origin.git"), "repo")
			repo := filepath.Join(base, "repo")
			os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base"), 0o644)
			run(t, repo, "git", "add", "base.txt")
			run(t, repo, "git", "commit", "-q", "-m", "root")
			run(t, repo, "git", "push", "-q", "origin", "main")
			w := Worktree{Path: WorktreePath(repo, "f"), Branch: "f", GitDir: filepath.Join(repo, ".git")}
			run(t, repo, "git", "worktree", "add", "-q", "-b", "f", w.Path)
			c.work(t, w.Path)
			want := map[string]string{}
			for _, f := range []string{"base.txt", "done.txt", "new.txt"} {
				if b, err := os.ReadFile(filepath.Join(w.Path, f)); err == nil {
					want[f] = string(b)
				}
			}
			if got, err := Weigh(w.Path); err != nil || got != c.toll {
				t.Fatalf("Weigh = %+v, %v; want %+v", got, err, c.toll)
			}
			head := Head(w.Path)
			r, err := Stash(w.Path, "f", filepath.Join(base, "state", "recovery"))
			if err != nil {
				t.Fatal(err)
			}
			if (r.Bundle != "") != c.bundle {
				t.Fatalf("bundle %q, want one: %v", r.Bundle, c.bundle)
			}
			run(t, repo, "git", "worktree", "remove", "--force", w.Path)
			run(t, repo, "git", "branch", "-q", "-D", "f")
			run(t, repo, "git", "reflog", "expire", "--expire=now", "--all")
			run(t, repo, "git", "gc", "-q", "--prune=now")

			if err := r.Fetch(w.GitDir, "f"); err != nil {
				t.Fatal(err)
			}
			if _, _, err := Restore(w, head, nil); err != nil {
				t.Fatal(err)
			}
			if err := r.Apply(w.Path, "f"); err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, f := range []string{"base.txt", "done.txt", "new.txt"} {
				if b, err := os.ReadFile(filepath.Join(w.Path, f)); err == nil {
					got[f] = string(b)
				}
			}
			if len(got) != len(want) {
				t.Fatalf("files %v, want %v", got, want)
			}
			for f, v := range want {
				if got[f] != v {
					t.Fatalf("files %v, want %v", got, want)
				}
			}
			if Head(w.Path) != head {
				t.Fatalf("head %s, want %s", Head(w.Path), head)
			}
			if r.Bundle != "" {
				if _, err := os.Stat(r.Bundle); err == nil {
					t.Fatalf("bundle %s stays after recovery", r.Bundle)
				}
			}
		})
	}
}
