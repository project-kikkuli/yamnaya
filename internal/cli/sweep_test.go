package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestTheBoardNeverRunsGitAndACleanupWaitsForTheProcessInItsWorktree(t *testing.T) {
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	for _, c := range []struct {
		name   string
		killed bool
	}{{"exited, a process working in its worktree", false}, {"killed a day ago with uncommitted work", true}} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "push", "-q", "origin", "main")
			wt := launch.WorktreePath(repo, "task")
			git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
			os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("half done"), 0o644)

			calls := filepath.Join(base, "git-calls")
			bins := filepath.Join(base, "bin")
			os.MkdirAll(bins, 0o755)
			os.WriteFile(filepath.Join(bins, "git"), []byte("#!/bin/sh\necho \"$*\" >> "+calls+"\nexec "+real+" \"$@\"\n"), 0o755)
			t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
			forks := func() int {
				b, _ := os.ReadFile(calls)
				os.Remove(calls)
				return strings.Count(string(b), "\n")
			}

			l := launcher{srv: &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(base, "state", "launches")}}
			rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now().Add(-72 * time.Hour),
				Repo: "repo", Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
			stop := func() {}
			if c.killed {
				rec.Killed = time.Now().Add(-48 * time.Hour)
			} else {
				busy := exec.Command("sleep", "60")
				busy.Dir = wt
				if err := busy.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { busy.Process.Kill(); busy.Wait() })
				stop = func() { busy.Process.Kill(); busy.Wait() }
			}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if rows := l.Dormant(); len(rows) != 1 || forks() != 0 {
				t.Fatalf("board read rows %+v and ran git; want the row and no git", rows)
			}
			l.Sweep(core.Stopped(l.Launched()))
			after, _ := l.records.Find(rec.Key)
			if c.killed {
				_, statErr := os.Stat(wt)
				if !after.IsLanded() || statErr == nil || after.Recovery.Files != 1 {
					t.Fatalf("killed record landed %v, worktree kept %v, recovery %+v; want removed with its work bundled", after.IsLanded(), statErr == nil, after.Recovery)
				}
				return
			}
			if after.IsLanded() {
				t.Fatal("cleaned up a worktree a process works in")
			}
			for range 3 {
				l.Sweep(core.Stopped(l.Launched()))
				l.Dormant()
			}
			if n := forks(); n != 0 {
				t.Fatalf("ran git %d times while a process works in the worktree", n)
			}
			stop()
			l.Sweep(core.Stopped(l.Launched()))
			if after, _ := l.records.Find(rec.Key); !after.IsLanded() || after.Recovery.Files != 1 {
				t.Fatalf("once the process left, landed %v with recovery %+v; want cleaned up with its file bundled", after.IsLanded(), after.Recovery)
			}
		})
	}
}
