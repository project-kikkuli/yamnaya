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

// lfsAttributes puts *.png under filter=lfs and pushes that. The attribute alone decides, so no LFS driver need be installed.
func lfsAttributes(t *testing.T, wt string) {
	t.Helper()
	plant(t, wt, ".gitattributes", "*.png filter=lfs diff=lfs merge=lfs -text\n")
	git(t, wt, "add", ".gitattributes")
	git(t, wt, "commit", "-q", "-m", "lfs")
	git(t, wt, "push", "-q", "origin", "task")
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
			{"an untracked LFS file", func(t *testing.T, wt string) {
				lfsAttributes(t, wt)
				plant(t, wt, "art/a.png", "real image")
			}, "a bundle cannot hold: art/a.png", []string{"art/a.png"}, 1, 0},
			{"a changed tracked LFS file", func(t *testing.T, wt string) {
				lfsAttributes(t, wt)
				plant(t, wt, "a.png", "real image")
				git(t, wt, "add", "a.png")
				git(t, wt, "commit", "-q", "-m", "image")
				git(t, wt, "push", "-q", "origin", "task")
				plant(t, wt, "a.png", "another image")
			}, "a bundle cannot hold: a.png", []string{"a.png"}, 1, 0},
			{"a staged new LFS file", func(t *testing.T, wt string) {
				lfsAttributes(t, wt)
				plant(t, wt, "a.png", "real image")
				git(t, wt, "add", "a.png")
			}, "a bundle cannot hold: a.png", []string{"a.png"}, 1, 0},
			{"an untracked file under no filter beside the LFS attribute", func(t *testing.T, wt string) {
				lfsAttributes(t, wt)
				plant(t, wt, "notes.txt")
			}, "", nil, 1, 0},
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
	if _, err := l.srv.Start(l.conf, "repo-wt-task", wt, []string{"sleep", "600"}); err != nil {
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
	if p, err := l.srv.Pane("repo-wt-task"); err == nil {
		t.Fatalf("the pane outlived the end: %+v", p)
	}

	rec.Killed = time.Now().Add(-48 * time.Hour)
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	if err := l.Sweep(core.Stopped(l.Launched())); err == nil {
		t.Fatal("serve's first try said nothing of the failure")
	}
	for range 3 {
		if err := l.Sweep(core.Stopped(l.Launched())); err != nil {
			t.Fatalf("serve repeated a failure nothing changed: %v", err)
		}
	}
	if after, _ := l.records.Find(rec.Key); after.IsLanded() || after.Blocked == "" {
		t.Fatalf("landed %v, blocked %q; want it held back with why", after.IsLanded(), after.Blocked)
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
		{"staged, marked assume-unchanged and edited since", func(t *testing.T, repo, wt, base string) {
			git(t, wt, "add", "-A")
			git(t, wt, "update-index", "--assume-unchanged", "notes.txt")
			plant(t, wt, "notes.txt", "edited behind the flag")
		}, false, true, "stays in", []string{".env", "notes.txt"}},
		{"staged, marked skip-worktree and removed since", func(t *testing.T, repo, wt, base string) {
			git(t, wt, "add", "-A")
			git(t, wt, "update-index", "--skip-worktree", "notes.txt")
			os.Remove(filepath.Join(wt, "notes.txt"))
		}, false, true, "stays in", []string{".env"}},
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
			after, _ := l.records.Find(rec.Key)
			if after.IsLanded() != c.refused || (after.Recovery != (launch.Recovery{})) != c.bundle {
				t.Errorf("record landed %v, recovery %+v", after.IsLanded(), after.Recovery)
			}
			if c.refused && after.Head != ended.Head {
				t.Errorf("a refused revive recorded the other checkout's head %s over %s", after.Head, ended.Head)
			}
		})
	}
}

func TestReviveKeepsABundleWhoseCommitsTheWorktreeNoLongerHolds(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name   string
		change func(t *testing.T, repo, wt string)
		bundle bool
		note   string
	}{
		{"left alone", func(*testing.T, string, string) {}, false, ""},
		{"moved on with another commit", func(t *testing.T, repo, wt string) {
			git(t, wt, "commit", "-q", "--allow-empty", "-m", "more")
		}, false, ""},
		{"amended", func(t *testing.T, repo, wt string) {
			git(t, wt, "commit", "-q", "--amend", "--allow-empty", "-m", "rewritten")
		}, true, "stays in"},
		{"rebased", func(t *testing.T, repo, wt string) {
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "main moved")
			git(t, wt, "rebase", "-q", "main")
		}, true, "stays in"},
		{"reset to main", func(t *testing.T, repo, wt string) {
			git(t, wt, "reset", "-q", "--hard", "origin/main")
		}, true, "stays in"},
		{"reset to main and the commit collected", func(t *testing.T, repo, wt string) {
			git(t, wt, "reset", "-q", "--hard", "origin/main")
			gitDir := filepath.Join(repo, ".git", "worktrees", filepath.Base(wt))
			os.Remove(filepath.Join(gitDir, "ORIG_HEAD"))
			git(t, repo, "reflog", "expire", "--expire=now", "--all")
			git(t, repo, "gc", "-q", "--prune=now")
		}, true, "stays in"},
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
			git(t, wt, "commit", "-q", "--allow-empty", "-m", "only here")
			only := launch.Head(wt)
			l := testLauncher(t, base)
			rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(), Head: only,
				Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if _, err := l.End(rec.Key, ""); err != nil {
				t.Fatal(err)
			}
			ended, _ := l.records.Find(rec.Key)
			if ended.Recovery.Commits != 1 || ended.Recovery.Work != "" {
				t.Fatalf("want a bundle of commits alone, got %+v", ended.Recovery)
			}
			c.change(t, repo, wt)

			msg, err := l.Revive(rec.Key)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg, c.note) {
				t.Errorf("revive said %q, want %q", msg, c.note)
			}
			if _, err := os.Stat(ended.Recovery.Bundle); (err == nil) != c.bundle {
				t.Errorf("bundle on disk %v, want %v", err == nil, c.bundle)
			}
			after, _ := l.records.Find(rec.Key)
			if (after.Recovery != (launch.Recovery{})) != c.bundle {
				t.Errorf("recovery %+v, want one kept %v", after.Recovery, c.bundle)
			}
			if c.bundle {
				heads, err := exec.Command("git", "-C", repo, "bundle", "list-heads", ended.Recovery.Bundle).Output()
				if err != nil || !strings.Contains(string(heads), only) {
					t.Errorf("the bundle lists %q, %v; want %s", heads, err, only)
				}
				git(t, repo, "cat-file", "-e", only)
			}
		})
	}
}

func TestReviveSaysSoWhenTheBundleItWouldKeepIsGone(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
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
	os.Remove(ended.Recovery.Bundle)
	msg, err := l.Revive(rec.Key)
	if err != nil || !strings.Contains(msg, "is gone") || strings.Contains(msg, "stays in") {
		t.Fatalf("revive said %q, %v; want it to say the bundle is gone", msg, err)
	}
}

func TestReviveRestoresBinaryWorkHoweverItSorts(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	binary := "\x00\x01binary\x00\xff\n\n"
	for _, c := range []struct {
		name    string
		tracked bool
		after   bool
	}{
		{"an untracked binary sorting last", false, false},
		{"an untracked binary with text after it", false, true},
		{"a changed tracked binary sorting last", true, false},
		{"a changed tracked binary with text after it", true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			fakeAgent(t)
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "push", "-q", "origin", "main")
			wt := launch.WorktreePath(repo, "task")
			git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
			if c.tracked {
				plant(t, wt, "data.dat", "\x00before\x00")
				git(t, wt, "add", "data.dat")
				git(t, wt, "commit", "-q", "-m", "binary")
				git(t, wt, "push", "-q", "origin", "task")
			}
			plant(t, wt, "data.dat", binary)
			if c.after {
				plant(t, wt, "z.txt", "after\n")
			}
			l := testLauncher(t, base)
			rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(), Head: launch.Head(wt),
				Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if _, err := l.End(rec.Key, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(wt); err == nil {
				t.Fatal("the worktree stayed")
			}
			if _, err := l.Revive(rec.Key); err != nil {
				t.Fatalf("revive: %v", err)
			}
			if got, err := os.ReadFile(filepath.Join(wt, "data.dat")); err != nil || string(got) != binary {
				t.Fatalf("data.dat is %q, %v", got, err)
			}
			if c.after {
				if got, err := os.ReadFile(filepath.Join(wt, "z.txt")); err != nil || string(got) != "after\n" {
					t.Fatalf("z.txt is %q, %v", got, err)
				}
			}
		})
	}
}

func TestARealLFSFileKeepsItsWorktreeAtItsEnd(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	if _, err := exec.LookPath("git-lfs"); err != nil {
		t.Skip("git-lfs not installed")
	}
	fakeAgent(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	repo := filepath.Join(base, "repo")
	git(t, repo, "lfs", "install", "--local")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	wt := launch.WorktreePath(repo, "task")
	git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
	git(t, wt, "push", "-q", "origin", "task")
	lfsAttributes(t, wt)
	plant(t, wt, "a.png", "real image")
	l := testLauncher(t, base)
	rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(), Head: launch.Head(wt),
		Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
	if err := l.records.Save(rec); err != nil {
		t.Fatal(err)
	}
	msg, err := l.End(rec.Key, "")
	if err != nil || !strings.Contains(msg, "a.png") {
		t.Fatalf("the end said %q, %v", msg, err)
	}
	ended, _ := l.records.Find(rec.Key)
	if _, err := l.Revive(rec.Key); err != nil {
		t.Fatalf("revive: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(wt, "a.png")); err != nil || string(got) != "real image" {
		t.Fatalf("a.png is %q, %v", got, err)
	}
	if _, err := os.Stat(ended.Recovery.Bundle); err == nil {
		t.Error("left a bundle revive proved it no longer needs")
	}
}

func TestALaterEndNeverLosesABundleReviveKept(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name  string
		later func(t *testing.T, wt string)
	}{
		{"with new work to bundle", func(t *testing.T, wt string) { plant(t, wt, "new.txt", "later work") }},
		{"with nothing new to bundle", func(*testing.T, string) {}},
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
			git(t, wt, "commit", "-q", "--allow-empty", "-m", "only here")
			only := launch.Head(wt)
			l := testLauncher(t, base)
			rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(), Head: only,
				Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			collect := func() {
				os.Remove(filepath.Join(repo, ".git", "worktrees", filepath.Base(wt), "ORIG_HEAD"))
				git(t, repo, "reflog", "expire", "--expire=now", "--all")
				git(t, repo, "gc", "-q", "--prune=now")
			}
			if _, err := l.End(rec.Key, ""); err != nil {
				t.Fatal(err)
			}
			git(t, wt, "reset", "-q", "--hard", "origin/main")
			collect()
			if msg, err := l.Revive(rec.Key); err != nil || !strings.Contains(msg, "stays in") {
				t.Fatalf("first revive said %q, %v", msg, err)
			}

			c.later(t, wt)
			if _, err := l.End(rec.Key, ""); err != nil {
				t.Fatal(err)
			}
			collect()
			ended, _ := l.records.Find(rec.Key)
			if len(ended.Shelved) != 1 {
				t.Fatalf("shelved %+v, recovery %+v; want the earlier bundle kept", ended.Shelved, ended.Recovery)
			}
			earlier := ended.Shelved[0].Bundle
			heads, err := exec.Command("git", "-C", repo, "bundle", "list-heads", earlier).Output()
			if err != nil || !strings.Contains(string(heads), only) {
				t.Fatalf("the earlier bundle lists %q, %v; want %s", heads, err, only)
			}
			git(t, repo, "cat-file", "-e", only)

			msg, err := l.Revive(rec.Key)
			if err != nil || !strings.Contains(msg, "earlier saved work stays in "+earlier) {
				t.Fatalf("second revive said %q, %v; want the earlier bundle named", msg, err)
			}
			if _, err := os.Stat(earlier); err != nil {
				t.Fatalf("the earlier bundle went: %v", err)
			}
			if _, err := os.Stat(filepath.Join(wt, ".env")); err != nil {
				t.Fatalf(".env went: %v", err)
			}

			git(t, wt, "clean", "-fdq")
			git(t, wt, "reset", "-q", "--hard", only)
			if _, err := l.End(rec.Key, ""); err != nil {
				t.Fatal(err)
			}
			if msg, err := l.Revive(rec.Key); err != nil || strings.Contains(msg, earlier) {
				t.Fatalf("revive with the commit back said %q, %v; want the earlier bundle settled", msg, err)
			}
			if _, err := os.Stat(earlier); err == nil {
				t.Fatal("kept a bundle whose commits the worktree holds again")
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
