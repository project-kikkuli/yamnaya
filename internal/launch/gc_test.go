package launch

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestGCRemovesOnlyIdleUnusedWorktreesThatLoseNothing(t *testing.T) {
	commit := func(t *testing.T, wt string) { run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "work") }
	merged := func(t *testing.T, wt string) { commit(t, wt); run(t, wt, "git", "push", "-q", "origin", "HEAD:main") }
	pushed := func(t *testing.T, wt string) { commit(t, wt); run(t, wt, "git", "push", "-q", "origin", "HEAD") }
	ignored := func(t *testing.T, wt string) {
		merged(t, wt)
		os.WriteFile(filepath.Join(wt, ".gitignore"), []byte(".env\n"), 0o644)
		run(t, wt, "git", "add", ".gitignore")
		run(t, wt, "git", "commit", "-q", "-m", "ignore")
		run(t, wt, "git", "push", "-q", "origin", "HEAD:main")
		os.WriteFile(filepath.Join(wt, ".env"), []byte("SECRET=1"), 0o644)
	}
	dirty := func(t *testing.T, wt string) {
		merged(t, wt)
		os.WriteFile(filepath.Join(wt, "new.txt"), []byte("x"), 0o644)
	}
	old := 4 * 24 * time.Hour
	expired := func(t *testing.T, wt string) {
		merged(t, wt)
		admin, _ := git(wt, "rev-parse", "--absolute-git-dir")
		later := time.Now().Add(old)
		os.Chtimes(filepath.Join(admin, "logs", "HEAD"), later, later)
	}
	for _, c := range []struct {
		name   string
		setup  func(t *testing.T, wt string)
		age    time.Duration
		inside bool
		dry    bool
		keep   string
		branch bool
		detach bool
	}{
		{"merged, idle", merged, old, false, false, "", false, false},
		{"merged, idle, only ignored files", ignored, old, false, false, "", false, false},
		{"pushed not merged, idle", pushed, old, false, false, "", true, false},
		{"merged, touched today", merged, 0, false, false, KeepActive, true, false},
		{"merged, idle, reflog rewritten by git gc today", expired, old, false, false, "", false, false},
		{"merged, idle, a process works inside", merged, old, true, false, KeepInUse, true, false},
		{"dirty and merged, idle", dirty, old, false, false, KeepDirty, true, false},
		{"local-only commit, idle", commit, old, false, false, KeepLocal, true, false},
		{"merged, idle, dry run", merged, old, false, true, "", true, false},
		{"merged, idle, main checkout detached", merged, old, false, false, "", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
			repo := filepath.Join(base, "repo")
			run(t, base, "git", "init", "-q", "-b", "main", "repo")
			run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
			run(t, repo, "git", "remote", "add", "origin", filepath.Join(base, "origin.git"))
			run(t, repo, "git", "push", "-q", "origin", "main")
			run(t, repo, "git", "branch", "stale-merged")
			run(t, repo, "git", "branch", "stale-unmerged")
			run(t, repo, "git", "worktree", "add", "-q", "spare", "stale-unmerged")
			run(t, filepath.Join(repo, "spare"), "git", "commit", "-q", "--allow-empty", "-m", "spare")
			run(t, repo, "git", "worktree", "remove", "spare")
			wt := WorktreePath(repo, "feature")
			run(t, repo, "git", "worktree", "add", "-q", "-b", "feature", wt)
			c.setup(t, wt)
			if c.detach {
				run(t, repo, "git", "checkout", "-q", "--detach")
			}
			var cwds []string
			if c.inside {
				cwds = []string{filepath.Join(wt, "sub")}
			}

			s, err := GC(repo, time.Now().Add(c.age), cwds, nil, nil, c.dry, true)
			if err != nil || len(s.Errors) > 0 {
				t.Fatal(err, s.Errors)
			}
			if len(s.Trees) != 1 || s.Trees[0].Keep != c.keep {
				t.Fatalf("decided %+v, want keep %q", s.Trees, c.keep)
			}
			if c.keep == "" && s.Trees[0].Size == 0 {
				t.Fatalf("no size measured for %+v", s.Trees[0])
			}
			_, statErr := os.Stat(wt)
			if kept := statErr == nil; kept != (c.keep != "" || c.dry) {
				t.Fatalf("worktree kept %v with keep %q, dry %v", kept, c.keep, c.dry)
			}
			hasBranch := func(b string) bool {
				return exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b).Run() == nil
			}
			if hasBranch("feature") != c.branch {
				t.Fatalf("feature branch kept %v, want %v", hasBranch("feature"), c.branch)
			}
			if hasBranch("stale-merged") != c.dry || !hasBranch("stale-unmerged") || !hasBranch("main") {
				t.Fatalf("branches: stale-merged %v, stale-unmerged %v, main %v", hasBranch("stale-merged"), hasBranch("stale-unmerged"), hasBranch("main"))
			}
			if want := "stale-merged"; !slices.ContainsFunc(s.Branches, func(d Dropped) bool { return d.Name == want }) {
				t.Fatalf("deleted %v, want %s among them", s.Branches, want)
			}
		})
	}
}

func TestGCLimitedToOwnedWorktreesLeavesEveryOtherWorktreeAndBranch(t *testing.T) {
	for _, c := range []struct {
		name    string
		owned   bool
		removed []string
		deleted []string
	}{
		{"unlimited", false, []string{"mine", "theirs"}, []string{"mine", "mine-old", "their-old", "theirs"}},
		{"limited to owned", true, []string{"mine"}, []string{"mine", "mine-old"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
			repo := filepath.Join(base, "repo")
			run(t, base, "git", "init", "-q", "-b", "main", "repo")
			run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
			run(t, repo, "git", "remote", "add", "origin", filepath.Join(base, "origin.git"))
			run(t, repo, "git", "push", "-q", "origin", "main")
			run(t, repo, "git", "fetch", "-q", "origin")
			run(t, repo, "git", "branch", "mine-old")
			run(t, repo, "git", "branch", "their-old")
			trees := map[string]string{}
			for _, b := range []string{"mine", "theirs"} {
				trees[b] = WorktreePath(repo, b)
				run(t, repo, "git", "worktree", "add", "-q", "-b", b, trees[b])
			}
			var own *Owned
			if c.owned {
				own = &Owned{Paths: map[string]bool{trees["mine"]: true}, Branches: map[string]bool{"mine": true, "mine-old": true}}
			}

			s, err := GC(repo, time.Now().Add(Idle+time.Hour), nil, nil, own, false, false)
			if err != nil || len(s.Errors) > 0 {
				t.Fatal(err, s.Errors)
			}
			var removed, deleted []string
			for _, tr := range s.Trees {
				if tr.Keep == "" && tr.Head != "" && tr.Why != "" {
					removed = append(removed, tr.Branch)
				}
			}
			for _, d := range s.Branches {
				if d.Head != "" && d.Why == "merged into origin/main" {
					deleted = append(deleted, d.Name)
				}
			}
			slices.Sort(removed)
			slices.Sort(deleted)
			if !slices.Equal(removed, c.removed) || !slices.Equal(deleted, c.deleted) {
				t.Fatalf("removed %v and deleted %v, want %v and %v (%+v)", removed, deleted, c.removed, c.deleted, s)
			}
			for b, wt := range trees {
				if _, err := os.Stat(wt); (err != nil) != slices.Contains(c.removed, b) {
					t.Fatalf("%s worktree on disk: %v", b, err == nil)
				}
			}
		})
	}
}
