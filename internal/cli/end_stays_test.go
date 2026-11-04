package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/disk"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func plant(t *testing.T, dir, rel string, text ...string) {
	t.Helper()
	writeFile(t, filepath.Join(dir, rel), strings.Join(text, ""))
}

func nestedRepo(t *testing.T, wt, rel string) {
	t.Helper()
	inner := filepath.Join(wt, rel)
	if err := os.MkdirAll(inner, 0o755); err != nil {
		t.Fatal(err)
	}
	git(t, inner, "init", "-q")
	plant(t, inner, "n.txt", "only here")
	git(t, inner, "add", ".")
	git(t, inner, "commit", "-q", "-m", "only here")
}

func TestEndingASessionLeavesAWorktreeGCWouldKeepForWhatItHolds(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	type world struct {
		l   launcher
		rec launch.Record
	}
	for _, path := range []struct {
		name  string
		agent string
		end   func(t *testing.T, w world) string
	}{
		{"kill, worker and verifier ends", "sleep 600", func(t *testing.T, w world) string {
			msg, err := w.l.End(w.rec.Key, "")
			if err != nil {
				t.Fatal(err)
			}
			return msg
		}},
		{"serve's cleanup", "true", func(t *testing.T, w world) string {
			if err := w.l.Sweep(core.Stopped(w.l.Launched())); err != nil {
				t.Fatal(err)
			}
			return ""
		}},
	} {
		for _, c := range []struct {
			name    string
			setup   func(t *testing.T, wt string)
			why     string
			safe    []string
			files   int
			commits int
		}{
			{"nothing ignored", func(*testing.T, string) {}, "", nil, 0, 0},
			{"only regenerable ignored dirs", func(t *testing.T, wt string) {
				plant(t, wt, "node_modules/a.js")
			}, "", nil, 0, 0},
			{"an ignored .env", func(t *testing.T, wt string) {
				plant(t, wt, ".env")
			}, "holds git-ignored .env", []string{".env"}, 0, 0},
			{"an ignored .env and uncommitted work", func(t *testing.T, wt string) {
				plant(t, wt, ".env")
				plant(t, wt, "notes.txt")
			}, "holds git-ignored .env", []string{".env", "notes.txt"}, 1, 0},
			{"an ignored .env and an unpushed commit", func(t *testing.T, wt string) {
				plant(t, wt, ".env")
				git(t, wt, "commit", "-q", "--allow-empty", "-m", "unpushed")
			}, "holds git-ignored .env", []string{".env"}, 0, 1},
			{"an untracked repository with a commit", func(t *testing.T, wt string) {
				nestedRepo(t, wt, "nested")
			}, "holds a repository under nested/", []string{"nested/n.txt", "nested/.git"}, 1, 0},
			{"an untracked repository deep in an untracked directory", func(t *testing.T, wt string) {
				nestedRepo(t, wt, "a/b/nested")
			}, "holds a repository under a/", []string{"a/b/nested/n.txt"}, 1, 0},
			{"an untracked repository with no commit yet", func(t *testing.T, wt string) {
				git(t, wt, "init", "-q", "fresh")
				plant(t, wt, "fresh/n.txt")
			}, "holds a repository under fresh/", []string{"fresh/n.txt"}, 0, 0},
			{"an untracked directory with its own .git file", func(t *testing.T, wt string) {
				plant(t, wt, "linked/.git", "gitdir: /nowhere\n")
				plant(t, wt, "linked/n.txt")
			}, "holds a repository under linked/", []string{"linked/n.txt"}, 1, 0},
			{"an untracked Mercurial checkout", func(t *testing.T, wt string) {
				plant(t, wt, "vendor/.hg/store/data")
				plant(t, wt, "vendor/n.txt")
			}, "holds a repository under vendor/", []string{"vendor/.hg/store/data", "vendor/n.txt"}, 2, 0},
			{"a repository under an ignored directory", func(t *testing.T, wt string) {
				git(t, wt, "init", "-q", "scratch/clone")
			}, "holds a repository under scratch/", []string{"scratch/clone/.git"}, 0, 0},
		} {
			t.Run(path.name+"/"+c.name, func(t *testing.T) {
				fakeAgent(t)
				base, _ := filepath.EvalSymlinks(t.TempDir())
				git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
				git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
				repo := filepath.Join(base, "repo")
				plant(t, repo, ".gitignore", ".env\nnode_modules\nscratch\n")
				git(t, repo, "add", ".")
				git(t, repo, "commit", "-q", "-m", "root")
				git(t, repo, "push", "-q", "origin", "main")
				wt := launch.WorktreePath(repo, "task")
				git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
				git(t, wt, "push", "-q", "origin", "task")
				c.setup(t, wt)

				l := testLauncher(t, base)
				l.removals = filepath.Join(base, "gc.log")
				rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now().Add(-72 * time.Hour),
					Killed: time.Now().Add(-48 * time.Hour), Repo: "repo", Head: launch.Head(wt),
					Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
				if err := l.records.Save(rec); err != nil {
					t.Fatal(err)
				}
				if _, err := l.srv.Start(l.conf, "repo-wt-task", wt, []string{"sh", "-c", path.agent}); err != nil {
					t.Fatal(err)
				}
				for end := time.Now().Add(5 * time.Second); path.agent == "true"; time.Sleep(20 * time.Millisecond) {
					if p, err := l.srv.Pane("repo-wt-task"); err == nil && p.Dead {
						break
					}
					if time.Now().After(end) {
						t.Fatal("the agent never exited")
					}
				}

				msg := path.end(t, world{l, rec})

				if _, err := l.srv.Pane("repo-wt-task"); err == nil {
					t.Error("the pane outlived the end")
				}
				after, _ := l.records.Find(rec.Key)
				if !after.IsLanded() || after.Recovery.Files != c.files || after.Recovery.Commits != c.commits {
					t.Fatalf("landed %v, recovery %+v; want %d files and %d commits bundled", after.IsLanded(), after.Recovery, c.files, c.commits)
				}
				_, statErr := os.Stat(wt)
				if stays := statErr == nil; stays != (c.why != "") {
					t.Fatalf("worktree on disk %v, want %v", stays, c.why != "")
				}
				for _, f := range c.safe {
					if _, err := os.Stat(filepath.Join(wt, f)); err != nil {
						t.Errorf("%s lost: %v", f, err)
					}
				}
				if c.why == "" {
					return
				}
				landing, _ := os.ReadFile(l.landings)
				if !strings.Contains(string(landing), c.why) {
					t.Errorf("landing log %q does not say %q", landing, c.why)
				}
				if path.name != "serve's cleanup" && (!strings.Contains(msg, wt) || !strings.Contains(msg, c.why) || strings.Contains(msg, ", kept ")) {
					t.Errorf("the end said %q; want where the worktree stayed and %q, and no claim of a bundle", msg, c.why)
				}

				all, _ := l.gcWith([]string{repo}, true, false, launch.Policy{Idle: time.Nanosecond})
				if len(all) != 1 || len(all[0].Trees) != 1 || all[0].Trees[0].Keep == "" {
					t.Fatalf("a later gc decided %+v; want it kept", all)
				}

				msg, err := l.Revive(rec.Key)
				if err != nil {
					t.Fatalf("revive: %v", err)
				}
				if !strings.Contains(msg, wt) {
					t.Errorf("revive said %q", msg)
				}
				back, _ := l.records.Find(rec.Key)
				if back.IsLanded() || back.Recovery != (launch.Recovery{}) {
					t.Errorf("revived record landed %v, recovery %+v", back.IsLanded(), back.Recovery)
				}
				if after.Recovery.Bundle != "" {
					if _, err := os.Stat(after.Recovery.Bundle); err == nil {
						t.Error("left a bundle it no longer needs")
					}
				}
				for _, f := range c.safe {
					if _, err := os.Stat(filepath.Join(wt, f)); err != nil {
						t.Errorf("%s lost by revive: %v", f, err)
					}
				}
				if launch.Head(wt) != after.Head {
					t.Errorf("revive moved the worktree to %s from %s", launch.Head(wt), after.Head)
				}
			})
		}
	}
}

func TestEndingASessionWhoseWorkCannotBeBundledRefusesAndLosesNothing(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	repo := filepath.Join(base, "repo")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	wt := launch.WorktreePath(repo, "task")
	git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
	plant(t, wt, "secret.txt", "only here")
	unreadable := filepath.Join(wt, "secret.txt")
	os.Chmod(unreadable, 0)
	t.Cleanup(func() { os.Chmod(unreadable, 0o644) })
	l := testLauncher(t, base)
	rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(), Head: launch.Head(wt),
		Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := l.End(rec.Key, ""); err == nil {
		t.Fatal("ended a session whose work it could not bundle")
	}
	if _, err := os.Stat(unreadable); err != nil {
		t.Fatalf("lost it: %v", err)
	}
	if after, _ := l.records.Find(rec.Key); after.IsLanded() {
		t.Fatal("landed a session whose work it could not bundle")
	}
}

func TestReviveDropsTheBundleOnlyWhenTheWorktreeProvablyHoldsItsWork(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	readd := func(t *testing.T, repo, wt string, args ...string) {
		git(t, repo, "worktree", "remove", "--force", "--force", wt)
		git(t, repo, append([]string{"worktree", "add", "-q"}, args...)...)
	}
	for _, c := range []struct {
		name    string
		change  func(t *testing.T, repo, wt, base string)
		refused bool
		bundle  bool
		note    string
		has     []string
	}{
		{"left as it stayed", func(*testing.T, string, string, string) {}, false, false, "", []string{".env", "notes.txt"}},
		{"its work committed since", func(t *testing.T, repo, wt, base string) {
			git(t, wt, "add", "notes.txt")
			git(t, wt, "commit", "-q", "-m", "work")
		}, false, false, "", []string{".env", "notes.txt"}},
		{"edited since", func(t *testing.T, repo, wt, base string) {
			plant(t, wt, "later.txt", "later")
		}, false, true, "stays in", []string{".env", "notes.txt", "later.txt"}},
		{"removed by hand and added again on its branch", func(t *testing.T, repo, wt, base string) {
			readd(t, repo, wt, wt, "task")
		}, false, false, "", []string{"notes.txt"}},
		{"removed by hand and another branch added there", func(t *testing.T, repo, wt, base string) {
			readd(t, repo, wt, "-b", "other", wt)
		}, true, true, "", nil},
		{"replaced by an unrelated checkout", func(t *testing.T, repo, wt, base string) {
			git(t, repo, "worktree", "remove", "--force", "--force", wt)
			git(t, base, "init", "-q", "-b", "task", wt)
			git(t, wt, "commit", "-q", "--allow-empty", "-m", "unrelated")
		}, true, true, "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeAgent(t)
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
			repo := filepath.Join(base, "repo")
			plant(t, repo, ".gitignore", ".env\n")
			git(t, repo, "add", ".")
			git(t, repo, "commit", "-q", "-m", "root")
			git(t, repo, "push", "-q", "origin", "main")
			wt := launch.WorktreePath(repo, "task")
			git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
			plant(t, wt, ".env", "SECRET=1")
			plant(t, wt, "notes.txt", "half done")
			l := testLauncher(t, base)
			rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(), Head: launch.Head(wt),
				Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if _, err := l.End(rec.Key, ""); err != nil {
				t.Fatal(err)
			}
			ended, _ := l.records.Find(rec.Key)
			if ended.Recovery.Bundle == "" {
				t.Fatalf("no bundle: %+v", ended.Recovery)
			}
			c.change(t, repo, wt, base)

			msg, err := l.Revive(rec.Key)
			if (err != nil) != c.refused {
				t.Fatalf("revive said %q, %v; want refused %v", msg, err, c.refused)
			}
			if c.refused && !strings.Contains(err.Error(), ended.Recovery.Bundle) {
				t.Errorf("the refusal %q does not name the bundle", err)
			}
			if !strings.Contains(msg, c.note) {
				t.Errorf("revive said %q, want %q", msg, c.note)
			}
			if _, err := os.Stat(ended.Recovery.Bundle); (err == nil) != c.bundle {
				t.Errorf("bundle on disk %v, want %v", err == nil, c.bundle)
			}
			for _, f := range c.has {
				if _, err := os.Stat(filepath.Join(wt, f)); err != nil {
					t.Errorf("%s missing after revive: %v", f, err)
				}
			}
			if after, _ := l.records.Find(rec.Key); after.IsLanded() != c.refused || (after.Recovery != (launch.Recovery{})) != c.bundle {
				t.Errorf("record landed %v, recovery %+v", after.IsLanded(), after.Recovery)
			}
		})
	}
}

func TestALaterGCRemovesAWorktreeThatStayedOnceNothingHoldsIt(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	repo := filepath.Join(base, "repo")
	plant(t, repo, ".gitignore", ".env\n")
	git(t, repo, "add", ".")
	git(t, repo, "commit", "-q", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	wt := launch.WorktreePath(repo, "task")
	git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
	plant(t, wt, ".env")
	l := testLauncher(t, base)
	l.removals, l.everywhere = filepath.Join(base, "gc.log"), true
	rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(), Head: launch.Head(wt),
		Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := l.End(rec.Key, ""); err != nil {
		t.Fatal(err)
	}
	reclaim := launch.Policy{Idle: time.Nanosecond}
	all, _ := l.gcWith([]string{repo}, false, false, reclaim)
	if all[0].Trees[0].Keep != launch.KeepIgnored {
		t.Fatalf("gc decided %+v with the .env there", all[0].Trees)
	}
	if p := readPinned(base); len(p.Trees) != 1 || p.Trees[0].Path != wt || p.Trees[0].Why != "holds git-ignored .env" || p.At.IsZero() {
		t.Fatalf("saved %+v", p)
	}
	os.Remove(filepath.Join(wt, ".env"))
	all, _ = l.gcWith([]string{repo}, false, false, reclaim)
	if all[0].Trees[0].Keep != "" {
		t.Fatalf("gc decided %+v with the .env gone", all[0].Trees)
	}
	if _, err := os.Stat(wt); err == nil {
		t.Fatal("the worktree stayed")
	}
	if p := readPinned(base); len(p.Trees) != 0 {
		t.Fatalf("still saved as pinned: %+v", p)
	}
}

func TestYmDiskSaysWhenItHasNoListOfPinnedWorktrees(t *testing.T) {
	for _, c := range []struct {
		name, file, want string
	}{
		{"before any gc", "", "not measured yet"},
		{"corrupt", "{not json", "cannot read"},
		{"no time", `{"Trees":null}`, "cannot read"},
		{"nothing pinned", `{"At":"2026-11-04T12:00:00Z"}`, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if c.file != "" {
				plant(t, dir, "pinned.json", c.file)
			}
			var b strings.Builder
			printDisk(&b, nil, diskState{}, readPinned(dir))
			if got := b.String(); c.want != "" && !strings.Contains(got, c.want) || c.want == "" && got != "" || strings.Contains(got, "0001-01-01") {
				t.Fatalf("ym disk said %q, want %q", got, c.want)
			}
		})
	}
}

func TestPinnedWorktreesAreNamedWithTheirReason(t *testing.T) {
	sweep := launch.Sweep{Path: "/w/held", Branch: "b", Keep: launch.KeepIgnored, Why: "holds git-ignored .env", Size: 2e9}
	var dry strings.Builder
	printSwept(&dry, []launch.Swept{{Trees: []launch.Sweep{sweep}}}, 0, true)
	if !strings.Contains(dry.String(), "/w/held (b): holds git-ignored .env") {
		t.Errorf("dry run said %q", dry.String())
	}
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	if err := savePinned(dir, []launch.Swept{{Trees: []launch.Sweep{sweep, {Path: "/w/quiet", Keep: launch.KeepActive}}}}, at); err != nil {
		t.Fatal(err)
	}
	var shown strings.Builder
	printDisk(&shown, nil, diskState{}, readPinned(dir))
	for _, want := range []string{"/w/held: holds git-ignored .env", "2.00 GB", at.Format(time.RFC3339)} {
		if !strings.Contains(shown.String(), want) {
			t.Errorf("ym disk said %q, want %q", shown.String(), want)
		}
	}
	if strings.Contains(shown.String(), "/w/quiet") {
		t.Errorf("ym disk named a worktree kept only for being active: %q", shown.String())
	}
	item := diskItem("h", disk.Usage{Path: "/x", Total: 1000, Avail: 50}, nil, true, nil, "gc.log", readPinned(dir))
	if !strings.Contains(item.Rec, "1 worktrees, 2.00 GB") {
		t.Errorf("the owner item said %q", item.Rec)
	}
}
