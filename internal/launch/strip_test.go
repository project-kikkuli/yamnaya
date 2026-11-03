package launch

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// reapRepo is a repository with a pushed main and a worktree on branch feature whose work is on origin, every directory
// ignored the way real projects ignore them.
func reapRepo(t *testing.T) (repo, wt string) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	run(t, base, "git", "init", "-q", "--bare", "-b", "main", "origin.git")
	repo = filepath.Join(base, "repo")
	run(t, base, "git", "init", "-q", "-b", "main", "repo")
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules\n__pycache__\n.env\n*.tsbuildinfo\n.venv\ndist\n"), 0o644)
	run(t, repo, "git", "add", ".gitignore")
	run(t, repo, "git", "commit", "-q", "-m", "root")
	run(t, repo, "git", "remote", "add", "origin", filepath.Join(base, "origin.git"))
	run(t, repo, "git", "push", "-q", "origin", "main")
	wt = WorktreePath(repo, "feature")
	run(t, repo, "git", "worktree", "add", "-q", "-b", "feature", wt)
	return repo, wt
}

func put(t *testing.T, wt, rel string) {
	t.Helper()
	p := filepath.Join(wt, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func exists(wt, rel string) bool {
	_, err := os.Stat(filepath.Join(wt, rel))
	return err == nil
}

func TestATier2ReapNeverLosesWork(t *testing.T) {
	commit := func(t *testing.T, wt string) string {
		run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "work")
		return Head(wt)
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, repo, wt string) (*Squash, []string)
		keep  string
	}{
		{"plain merged and idle", func(t *testing.T, repo, wt string) (*Squash, []string) { return nil, nil }, ""},
		{"only regenerable ignored files", func(t *testing.T, repo, wt string) (*Squash, []string) {
			put(t, wt, "node_modules/a/b.js")
			put(t, wt, "pkg/__pycache__/x.pyc")
			put(t, wt, "app.tsbuildinfo")
			return nil, nil
		}, ""},
		{"dirty", func(t *testing.T, repo, wt string) (*Squash, []string) {
			os.WriteFile(filepath.Join(wt, ".gitignore"), []byte("changed\n"), 0o644)
			return nil, nil
		}, KeepDirty},
		{"untracked", func(t *testing.T, repo, wt string) (*Squash, []string) {
			put(t, wt, "notes.txt")
			return nil, nil
		}, KeepDirty},
		{"untracked hidden by config", func(t *testing.T, repo, wt string) (*Squash, []string) {
			put(t, wt, "notes.txt")
			run(t, repo, "git", "config", "status.showUntrackedFiles", "no")
			return nil, nil
		}, KeepDirty},
		{"unpushed", func(t *testing.T, repo, wt string) (*Squash, []string) {
			commit(t, wt)
			return nil, nil
		}, KeepLocal},
		{"ignored .env", func(t *testing.T, repo, wt string) (*Squash, []string) {
			put(t, wt, ".env")
			return nil, nil
		}, KeepIgnored},
		{"ignored build output not on the allowlist", func(t *testing.T, repo, wt string) (*Squash, []string) {
			put(t, wt, "dist/bundle.js")
			return nil, nil
		}, KeepIgnored},
		{"cwd inside", func(t *testing.T, repo, wt string) (*Squash, []string) {
			return nil, []string{filepath.Join(wt, "sub")}
		}, KeepInUse},
		{"locked", func(t *testing.T, repo, wt string) (*Squash, []string) {
			run(t, repo, "git", "worktree", "lock", wt)
			return nil, nil
		}, KeepLocked},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, wt := reapRepo(t)
			q, cwds := c.setup(t, repo, wt)
			head := Head(wt)
			s, err := GCWith(repo, time.Now().Add(2*time.Hour), cwds, q, nil, false, false, Policy{Idle: time.Hour, Ignored: true})
			if err != nil || len(s.Errors) > 0 {
				t.Fatal(err, s.Errors)
			}
			if len(s.Trees) != 1 || s.Trees[0].Keep != c.keep {
				t.Fatalf("decided %+v, want keep %q", s.Trees, c.keep)
			}
			if _, err := os.Stat(wt); (err == nil) != (c.keep != "") {
				t.Fatalf("worktree on disk %v with keep %q", err == nil, c.keep)
			}
			if c.keep != "" && Head(wt) != head {
				t.Fatalf("head moved from %s to %s", head, Head(wt))
			}
		})
	}
	squashCases := []struct {
		name  string
		setup func(t *testing.T, repo, wt, head string) *hub
		fail  bool
		keep  string
	}{
		{"squash-merged", func(t *testing.T, repo, wt, head string) *hub {
			return &hub{byBranch: map[string][]PR{"feature": {pr(7, head)}}}
		}, false, ""},
		{"squash-merged then more commits", func(t *testing.T, repo, wt, head string) *hub {
			commit(t, wt)
			return &hub{byBranch: map[string][]PR{"feature": {pr(7, head)}}}
		}, false, KeepLocal},
		{"gh failing", func(t *testing.T, repo, wt, head string) *hub { return &hub{} }, true, KeepLocal},
	}
	for _, c := range squashCases {
		t.Run(c.name, func(t *testing.T) {
			repo, wt, head := squashRepo(t)
			h := c.setup(t, repo, wt, head)
			var hb Hub = h
			if c.fail {
				hb = failingHub{}
			}
			q := &Squash{Hub: hb, Cache: filepath.Join(t.TempDir(), "squash.json"), Now: time.Now}
			s, err := GCWith(repo, time.Now().Add(2*time.Hour), nil, q, nil, false, false, Policy{Idle: time.Hour, Ignored: true})
			if err != nil {
				t.Fatal(err)
			}
			if len(s.Trees) != 1 || s.Trees[0].Keep != c.keep {
				t.Fatalf("decided %+v, want keep %q", s.Trees, c.keep)
			}
			if c.fail && len(s.Errors) == 0 {
				t.Fatal("gh failing left no error")
			}
			if c.fail && slices.ContainsFunc(s.Branches, func(d Dropped) bool { return d.Name == "feature" }) {
				t.Fatal("gh failing deleted the branch")
			}
		})
	}
}

type failingHub struct{}

func (failingHub) Merged(_, _ string) ([]PR, error)      { return nil, os.ErrDeadlineExceeded }
func (failingHub) Holding(_, _ string) ([]PR, error)     { return nil, os.ErrDeadlineExceeded }
func (failingHub) Ancestor(_, _, _ string) (bool, error) { return false, os.ErrDeadlineExceeded }

func TestTheOrdinaryGCStillRemovesAWorktreeHoldingIgnoredFilesAndAShortIdleOnlyAppliesToAReclaim(t *testing.T) {
	repo, wt := reapRepo(t)
	put(t, wt, ".env")
	s, err := GC(repo, time.Now().Add(2*time.Hour), nil, nil, nil, true, false)
	if err != nil || s.Trees[0].Keep != KeepActive {
		t.Fatalf("2h idle under the ordinary gc: %+v %v", s.Trees, err)
	}
	s, err = GC(repo, time.Now().Add(Idle+time.Hour), nil, nil, nil, true, false)
	if err != nil || s.Trees[0].Keep != "" {
		t.Fatalf("3 days idle under the ordinary gc: %+v %v", s.Trees, err)
	}
}

func TestAReapStopsRemovingOnceTheDiskHasEnough(t *testing.T) {
	repo, wt := reapRepo(t)
	s, err := GCWith(repo, time.Now().Add(2*time.Hour), nil, nil, nil, false, false, Policy{Idle: time.Hour, Ignored: true, Enough: func() bool { return true }})
	if err != nil || s.Trees[0].Keep != KeepEnough {
		t.Fatalf("%+v %v", s.Trees, err)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatal("removed a worktree with enough free")
	}
}

func TestStrippingTakesOnlyAllowlistedIgnoredEntriesAndLeavesWorkAlone(t *testing.T) {
	repo, wt := reapRepo(t)
	run(t, wt, "git", "commit", "-q", "--allow-empty", "-m", "unpushed")
	head := Head(wt)
	gone := []string{"node_modules/a/b.js", "web/node_modules/c.js", "pkg/__pycache__/x.pyc", "app.tsbuildinfo", ".venv/lib/y.py"}
	kept := []string{".env", "dist/bundle.js", "notes.txt", ".gitignore"}
	for _, f := range append(gone, ".env", "dist/bundle.js", "notes.txt") {
		put(t, wt, f)
	}
	idle := time.Now().Add(-24 * time.Hour)
	os.Chtimes(wt, idle, idle)
	s, err := GCWith(repo, time.Now(), nil, nil, nil, true, false, Policy{Idle: time.Hour, Ignored: true})
	if err != nil || len(s.Trees) != 1 || s.Trees[0].Keep == "" {
		t.Fatalf("%+v %v", s.Trees, err)
	}
	dry := StripIgnored(s.Trees[0], true)
	if dry.Err != nil || dry.Size == 0 || !exists(wt, "node_modules/a/b.js") {
		t.Fatalf("dry %+v", dry)
	}
	got := StripIgnored(s.Trees[0], false)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	for _, f := range gone {
		if exists(wt, f) {
			t.Errorf("%s survived", f)
		}
	}
	for _, f := range kept {
		if !exists(wt, f) {
			t.Errorf("%s removed", f)
		}
	}
	if Head(wt) != head {
		t.Fatal("head moved")
	}
	if fi, err := os.Stat(wt); err != nil || !fi.ModTime().Equal(idle) {
		t.Fatalf("the strip touched the worktree: %v %v", fi.ModTime(), err)
	}
	if got.Why() == "" {
		t.Fatal("nothing named for the log")
	}
}

func TestAnEntryTrackedInsideAnIgnoredDirectoryKeepsThatDirectory(t *testing.T) {
	_, wt := reapRepo(t)
	put(t, wt, "node_modules/patched.js")
	put(t, wt, "node_modules/other.js")
	run(t, wt, "git", "add", "-f", "node_modules/patched.js")
	run(t, wt, "git", "commit", "-q", "-m", "vendored one file")
	run(t, wt, "git", "push", "-q", "origin", "HEAD")
	got := StripIgnored(Sweep{Path: wt}, false)
	if got.Err != nil {
		t.Fatal(got.Err)
	}
	if !exists(wt, "node_modules/patched.js") {
		t.Fatal("a tracked file was stripped")
	}
}

func TestAVirtualenvIgnoringItselfCountsAsRegenerable(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	run(t, base, "git", "init", "-q", "-b", "main", "repo")
	repo := filepath.Join(base, "repo")
	run(t, repo, "git", "commit", "-q", "--allow-empty", "-m", "root")
	put(t, repo, ".venv/.gitignore")
	os.WriteFile(filepath.Join(repo, ".venv", ".gitignore"), []byte("*\n"), 0o644)
	put(t, repo, ".venv/lib/x.py")
	held, err := unregenerable(repo)
	if err != nil || len(held) != 0 {
		t.Fatalf("held %v, %v", held, err)
	}
	StripIgnored(Sweep{Path: repo}, false)
	if exists(repo, ".venv/lib/x.py") {
		t.Fatal("a self-ignoring virtualenv survived")
	}
}

func TestStrippableOnlyWhenKeptIdleAndUnused(t *testing.T) {
	for _, c := range []struct {
		name string
		w    Sweep
		want bool
	}{
		{"dirty and idle", Sweep{Keep: KeepDirty, Idle: StripIdle}, true},
		{"local commits and idle", Sweep{Keep: KeepLocal, Idle: StripIdle}, true},
		{"ignored files and idle", Sweep{Keep: KeepIgnored, Idle: StripIdle}, true},
		{"touched lately", Sweep{Keep: KeepDirty, Idle: StripIdle - time.Minute}, false},
		{"a process works inside", Sweep{Keep: KeepDirty, Idle: StripIdle, User: "/x"}, false},
		{"locked", Sweep{Keep: KeepLocked, Idle: StripIdle}, false},
		{"removed", Sweep{Idle: StripIdle}, false},
	} {
		if got := Strippable(c.w); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
