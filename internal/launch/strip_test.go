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
	os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("node_modules\n__pycache__\n.env\n*.tsbuildinfo\n.venv\ndist\ntarget\nprod.env\nscratch\n.claude\n"), 0o644)
	for _, f := range []string{"pkg/mod.py", "web/app.js"} {
		put(t, repo, f)
	}
	run(t, repo, "git", "add", ".")
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
	s, err := GCWith(repo, time.Now().Add(2*time.Hour), nil, nil, nil, false, false, Policy{Idle: time.Hour, Ignored: true, Enough: func(string) bool { return true }})
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
	kept := []string{".env", "dist/bundle.js", "notes.txt", ".gitignore", "pkg/mod.py", "web/app.js"}
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
	put(t, repo, ".venv/pyvenv.cfg")
	put(t, repo, ".venv/lib/x.py")
	if keep, why := keepContents(repo, true); keep != "" {
		t.Fatalf("kept: %s %s", keep, why)
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
		{"session not landed, no pane", Sweep{Keep: KeepRecord, Idle: StripIdle}, true},
		{"removed", Sweep{Idle: StripIdle}, false},
	} {
		if got := Strippable(c.w); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

func TestNeitherReapNorStripTouchesWhatIsNotARegenerableWholeDirectory(t *testing.T) {
	nested := func(t *testing.T, wt, dir string) {
		t.Helper()
		put(t, wt, filepath.Join(dir, "src.txt"))
		run(t, filepath.Join(wt, dir), "git", "init", "-q")
		run(t, filepath.Join(wt, dir), "git", "add", ".")
		run(t, filepath.Join(wt, dir), "git", "commit", "-q", "-m", "local only")
		put(t, wt, filepath.Join(dir, "uncommitted.txt"))
	}
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, repo, wt string)
		keep  string
		safe  []string
		gone  []string
	}{
		{"editable install with a repo inside a virtualenv", func(t *testing.T, repo, wt string) {
			put(t, wt, ".venv/pyvenv.cfg")
			nested(t, wt, ".venv/src/pkg")
		}, KeepNested, []string{".venv/src/pkg/src.txt", ".venv/src/pkg/uncommitted.txt"}, nil},
		{"clone in an ignored dir with a target inside", func(t *testing.T, repo, wt string) {
			nested(t, wt, "scratch/clone")
			put(t, wt, "scratch/clone/src/target/mod.rs")
		}, KeepNested, []string{"scratch/clone/src/target/mod.rs", "scratch/clone/uncommitted.txt"}, nil},
		{"linked worktree nested in an ignored dir", func(t *testing.T, repo, wt string) {
			inner := filepath.Join(wt, ".claude", "worktrees", "inner")
			run(t, repo, "git", "worktree", "add", "-q", "-b", "inner", inner)
			put(t, inner, "deploy/target/untracked.txt")
		}, KeepNested, []string{".claude/worktrees/inner/deploy/target/untracked.txt"}, nil},
		{"secret in an ignored file beside a tracked file under target", func(t *testing.T, repo, wt string) {
			put(t, wt, "deploy/target/keep.txt")
			run(t, wt, "git", "add", "-f", "deploy/target/keep.txt")
			run(t, wt, "git", "commit", "-q", "-m", "tracked under target")
			run(t, wt, "git", "push", "-q", "origin", "HEAD")
			put(t, wt, "deploy/target/prod.env")
		}, KeepIgnored, []string{"deploy/target/prod.env"}, nil},
		{"notes in a target with no build file beside it", func(t *testing.T, repo, wt string) {
			put(t, wt, "target/notes.md")
		}, KeepIgnored, []string{"target/notes.md"}, nil},
		{"pycache in a directory git folds whole", func(t *testing.T, repo, wt string) {
			put(t, wt, "scratch/__pycache__/x.pyc")
			put(t, wt, "scratch/data.bin")
		}, KeepIgnored, []string{"scratch/__pycache__/x.pyc", "scratch/data.bin"}, nil},
		{"a real build's target", func(t *testing.T, repo, wt string) {
			put(t, wt, "Cargo.toml")
			run(t, wt, "git", "add", "Cargo.toml")
			run(t, wt, "git", "commit", "-q", "-m", "crate")
			run(t, wt, "git", "push", "-q", "origin", "HEAD")
			put(t, wt, "target/debug/app")
		}, "", nil, []string{"target/debug/app"}},
		{"a repo inside node_modules", func(t *testing.T, repo, wt string) {
			nested(t, wt, "node_modules/gitdep")
		}, KeepNested, []string{"node_modules/gitdep/uncommitted.txt"}, nil},
		{"a bare mirror inside a virtualenv", func(t *testing.T, repo, wt string) {
			put(t, wt, ".venv/pyvenv.cfg")
			os.MkdirAll(filepath.Join(wt, ".venv"), 0o755)
			run(t, filepath.Join(wt, ".venv"), "git", "init", "-q", "--bare", "mirror.git")
		}, KeepNested, []string{".venv/mirror.git/HEAD"}, nil},
		{"a bare mirror inside node_modules", func(t *testing.T, repo, wt string) {
			os.MkdirAll(filepath.Join(wt, "node_modules"), 0o755)
			run(t, filepath.Join(wt, "node_modules"), "git", "init", "-q", "--bare", "mirror.git")
		}, KeepNested, []string{"node_modules/mirror.git/HEAD"}, nil},
		{"a mercurial checkout inside a virtualenv", func(t *testing.T, repo, wt string) {
			put(t, wt, ".venv/src/pkg/.hg/store/data")
			put(t, wt, ".venv/src/pkg/mod.py")
		}, KeepNested, []string{".venv/src/pkg/mod.py"}, nil},
		{"a jujutsu checkout inside node_modules", func(t *testing.T, repo, wt string) {
			put(t, wt, "node_modules/dep/.jj/repo/store")
			put(t, wt, "node_modules/dep/edited.js")
		}, KeepNested, []string{"node_modules/dep/edited.js"}, nil},
		{"a subversion checkout inside node_modules", func(t *testing.T, repo, wt string) {
			put(t, wt, "node_modules/dep/.svn/wc.db")
			put(t, wt, "node_modules/dep/edited.js")
		}, KeepNested, []string{"node_modules/dep/edited.js"}, nil},
		{"target beside a tracked build file in a subdirectory", func(t *testing.T, repo, wt string) {
			put(t, wt, "src/Cargo.toml")
			run(t, wt, "git", "add", "src/Cargo.toml")
			run(t, wt, "git", "commit", "-q", "-m", "crate")
			run(t, wt, "git", "push", "-q", "origin", "HEAD")
			put(t, wt, "src/target/debug/app")
		}, "", nil, []string{"src/target/debug/app"}},
		{"target beside an untracked build file", func(t *testing.T, repo, wt string) {
			put(t, wt, "src/keep.rs")
			run(t, wt, "git", "add", "src/keep.rs")
			run(t, wt, "git", "commit", "-q", "-m", "source")
			run(t, wt, "git", "push", "-q", "origin", "HEAD")
			put(t, wt, "src/Cargo.toml")
			put(t, wt, "src/target/kept.rs")
		}, KeepDirty, []string{"src/target/kept.rs", "src/Cargo.toml"}, nil},
		{"a file left in a tool-owned directory goes with it", func(t *testing.T, repo, wt string) {
			put(t, wt, "node_modules/notes.md")
		}, "", nil, []string{"node_modules/notes.md"}},
	} {
		t.Run(c.name+"/reap", func(t *testing.T) {
			repo, wt := reapRepo(t)
			c.setup(t, repo, wt)
			s, err := GCWith(repo, time.Now().Add(2*time.Hour), nil, nil, nil, false, false, Policy{Idle: time.Hour, Ignored: true})
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(s.Trees, func(w Sweep) bool { return w.Path == wt })
			if i < 0 || s.Trees[i].Keep != c.keep {
				t.Fatalf("decided %+v, want keep %q", s.Trees, c.keep)
			}
			for _, f := range c.safe {
				if !exists(wt, f) {
					t.Errorf("%s lost", f)
				}
			}
			if _, err := os.Stat(wt); (err == nil) != (c.keep != "") {
				t.Errorf("worktree on disk %v with keep %q", err == nil, c.keep)
			}
		})
		t.Run(c.name+"/ordinary gc keeps repositories", func(t *testing.T) {
			if c.keep != KeepNested {
				t.Skip("not a nested repository")
			}
			repo, wt := reapRepo(t)
			c.setup(t, repo, wt)
			s, err := GC(repo, time.Now().Add(Idle+time.Hour), nil, nil, nil, false, false)
			if err != nil {
				t.Fatal(err)
			}
			i := slices.IndexFunc(s.Trees, func(w Sweep) bool { return w.Path == wt })
			if i < 0 || s.Trees[i].Keep != KeepNested {
				t.Fatalf("decided %+v", s.Trees)
			}
		})
		t.Run(c.name+"/strip", func(t *testing.T) {
			repo, wt := reapRepo(t)
			c.setup(t, repo, wt)
			got := StripIgnored(Sweep{Path: wt}, false)
			if got.Err != nil {
				t.Fatal(got.Err)
			}
			for _, f := range c.safe {
				if !exists(wt, f) {
					t.Errorf("%s stripped", f)
				}
			}
			for _, f := range c.gone {
				if exists(wt, f) {
					t.Errorf("%s survived", f)
				}
			}
		})
	}
}

func TestASweepHeldUpByGhOrGitEndsAndKeepsTheWork(t *testing.T) {
	sleeper := func(t *testing.T, name string) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	t.Run("gh", func(t *testing.T) {
		repo, wt, _ := squashRepo(t)
		sleeper(t, "gh")
		old := ghLimit
		ghLimit = 200 * time.Millisecond
		defer func() { ghLimit = old }()
		q := &Squash{Hub: GH{}, Cache: filepath.Join(t.TempDir(), "squash.json"), Now: time.Now}
		start := time.Now()
		s, err := GCWith(repo, time.Now().Add(2*time.Hour), nil, q, nil, false, false, Policy{Idle: time.Hour, Ignored: true})
		if err != nil || time.Since(start) > 30*time.Second {
			t.Fatalf("%v after %v", err, time.Since(start))
		}
		if len(s.Trees) != 1 || s.Trees[0].Keep != KeepLocal || len(s.Errors) == 0 {
			t.Fatalf("decided %+v, errors %v", s.Trees, s.Errors)
		}
		if _, err := os.Stat(wt); err != nil {
			t.Fatal("removed the worktree")
		}
	})
	t.Run("git", func(t *testing.T) {
		repo, _ := reapRepo(t)
		sleeper(t, "git")
		GitLimit = 200 * time.Millisecond
		defer func() { GitLimit = 0 }()
		start := time.Now()
		if _, err := GCWith(repo, time.Now(), nil, nil, nil, true, false, Policy{Idle: time.Hour}); err == nil {
			t.Fatal("a hung git reported no error")
		}
		if time.Since(start) > 30*time.Second {
			t.Fatalf("took %v", time.Since(start))
		}
	})
}

func TestAReadOfWhatIsInUseJustBeforeRemovalDecides(t *testing.T) {
	for _, c := range []struct {
		name  string
		fresh func(wt string) ([]string, error)
		keep  string
	}{
		{"nothing there", func(string) ([]string, error) { return nil, nil }, ""},
		{"a process moved in", func(wt string) ([]string, error) { return []string{wt}, nil }, KeepInUse},
		{"the process table cannot be read", func(string) ([]string, error) { return nil, os.ErrPermission }, KeepFailing},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo, wt := reapRepo(t)
			s, err := GCWith(repo, time.Now().Add(2*time.Hour), nil, nil, nil, false, false,
				Policy{Idle: time.Hour, Fresh: func() ([]string, error) { return c.fresh(wt) }})
			if err != nil || s.Trees[0].Keep != c.keep {
				t.Fatalf("decided %+v, %v, want %q", s.Trees, err, c.keep)
			}
			if _, err := os.Stat(wt); (err == nil) != (c.keep != "") {
				t.Fatalf("on disk %v", err == nil)
			}
		})
	}
}

func TestAWorktreeWithSubmodulesIsKeptNotFailedAfterADryRunSaidOtherwise(t *testing.T) {
	repo, wt := reapRepo(t)
	sub := t.TempDir()
	run(t, sub, "git", "init", "-q", "-b", "main")
	run(t, sub, "git", "commit", "-q", "--allow-empty", "-m", "sub")
	run(t, wt, "git", "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "dep")
	run(t, wt, "git", "commit", "-q", "-m", "add submodule")
	run(t, wt, "git", "push", "-q", "origin", "HEAD")
	s, err := GC(repo, time.Now().Add(Idle+time.Hour), nil, nil, nil, true, false)
	if err != nil || s.Trees[0].Keep != KeepModules {
		t.Fatalf("dry run decided %+v, %v", s.Trees, err)
	}
}
