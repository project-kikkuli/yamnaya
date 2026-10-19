package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v %s", strings.Join(args, " "), err, out)
	}
}

func TestExitedLaunchesLandByThemselvesOnlyWhenNothingIsLost(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	commit := func(t *testing.T, wt string) { git(t, wt, "commit", "-q", "--allow-empty", "-m", "work") }
	push := func(t *testing.T, wt string) { commit(t, wt); git(t, wt, "push", "-q", "origin", "HEAD") }
	merged := func(t *testing.T, wt string) { commit(t, wt); git(t, wt, "push", "-q", "origin", "HEAD:main") }
	dirty := func(t *testing.T, wt string) { os.WriteFile(filepath.Join(wt, "new.txt"), []byte("x"), 0o644) }
	day := 25 * time.Hour
	for _, c := range []struct {
		name   string
		agent  string
		age    time.Duration
		state  string
		setup  func(t *testing.T, wt string)
		forget bool
		busy   bool
		landed bool
		tree   bool
		branch bool
	}{
		{"exit 0, pushed", "exit 0", 0, "", push, false, false, true, false, true},
		{"exit 0, merged", "exit 0", 0, "", merged, false, false, true, false, false},
		{"exit 0, dirty", "exit 0", 0, "exited 0", dirty, false, false, false, true, true},
		{"exit 0, unpushed", "exit 0", 0, "exited 0", commit, false, false, false, true, true},
		{"exit 3, pushed", "exit 3", 0, "", push, false, false, true, false, true},
		{"exit 3, unpushed", "exit 3", 0, "exited 3", commit, false, false, false, true, true},
		{"gone, merged", "", 0, "", merged, false, false, true, false, false},
		{"gone, dirty", "", 0, "gone", dirty, false, false, false, true, true},
		{"SIGTERM today, pushed", "kill -TERM $$", 0, "sigterm", push, false, false, false, true, true},
		{"SIGKILL today, pushed", "kill -KILL $$", 0, "sigkill", push, false, false, false, true, true},
		{"SIGTERM a day ago, pushed", "kill -TERM $$", day, "", push, false, false, true, false, true},
		{"SIGTERM a day ago, unpushed", "kill -TERM $$", day, "sigterm", commit, false, false, false, true, true},
		{"gone, merged, a process works inside", "", 0, "gone", merged, false, true, false, true, true},
		{"exit 0, merged, a process works inside", "exit 0", 0, "exited 0", merged, false, true, false, true, true},
		{"forget, unpushed", "exit 3", 0, "", commit, true, false, true, true, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "remote", "add", "origin", filepath.Join(base, "origin.git"))
			git(t, repo, "push", "-q", "origin", "main")
			wt := launch.WorktreePath(repo, "feature")
			git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
			c.setup(t, wt)
			if c.busy {
				sleep := exec.Command("sleep", "60")
				sleep.Dir = wt
				if err := sleep.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { sleep.Process.Kill(); sleep.Wait() })
			}

			srv := &tmux.Host{Socket: fmt.Sprintf("ymtest-%d-%d", os.Getpid(), time.Now().UnixNano()), Kind: tmux.Server}
			t.Cleanup(func() { exec.Command("tmux", "-L", srv.Socket, "kill-server").Run() })
			conf := filepath.Join(base, "tmux.conf")
			if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0o644); err != nil {
				t.Fatal(err)
			}
			l := launcher{srv: srv, records: launch.Records{Dir: filepath.Join(base, "launches")}, conf: conf}
			rec := launch.Record{Key: "repo-wt-feature", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now().Add(-c.age),
				Worktree: launch.Worktree{Path: wt, Branch: "feature", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if c.agent != "" {
				if _, err := srv.Start(conf, tmux.SessionName(rec.Key), wt, []string{"sh", "-c", c.agent}); err != nil {
					t.Fatal(err)
				}
				for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
					if p, err := srv.Pane(tmux.SessionName(rec.Key)); err == nil && p.Dead {
						break
					}
					if time.Now().After(end) {
						t.Fatal("the agent never exited")
					}
				}
			}
			if c.forget {
				if _, err := l.Forget(rec.Key); err != nil {
					t.Fatal(err)
				}
			}

			rows := l.Dormant()
			_, recorded := l.records.Find(rec.Key)
			_, statErr := os.Stat(wt)
			_, paneErr := srv.Pane(tmux.SessionName(rec.Key))
			if gone := len(rows) == 0 && !recorded && paneErr != nil; gone != c.landed || (statErr == nil) != c.tree {
				t.Fatalf("rows %+v, record kept %v, pane err %v, worktree kept %v; want landed %v, worktree kept %v",
					rows, recorded, paneErr, statErr == nil, c.landed, c.tree)
			}
			if len(rows) == 1 && rows[0].State != c.state {
				t.Fatalf("row state %q, want %q", rows[0].State, c.state)
			}
			if kept := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/feature").Run() == nil; kept != c.branch {
				t.Fatalf("branch kept %v, want %v", kept, c.branch)
			}
		})
	}
}
