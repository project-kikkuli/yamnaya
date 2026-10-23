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
	"github.com/project-kikkuli/yamnaya/internal/proc"
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
		killed bool
		forget bool
		busy   bool
		landed bool
		tree   bool
		branch bool
		goal   bool
		// deleted removes the worktree, its repository and the repository's origin.
		deleted bool
	}{
		{"exit 0, pushed", "exit 0", 0, "", push, false, false, false, true, false, true, false, false},
		{"exit 0, merged", "exit 0", 0, "", merged, false, false, false, true, false, false, false, false},
		{"exit 0, dirty", "exit 0", 0, "exited 0", dirty, false, false, false, false, true, true, false, false},
		{"exit 0, unpushed", "exit 0", 0, "exited 0", commit, false, false, false, false, true, true, false, false},
		{"exit 3, pushed", "exit 3", 0, "", push, false, false, false, true, false, true, false, false},
		{"exit 3, unpushed", "exit 3", 0, "exited 3", commit, false, false, false, false, true, true, false, false},
		{"gone, merged", "", 0, "", merged, false, false, false, true, false, false, false, false},
		{"gone, dirty", "", 0, "gone", dirty, false, false, false, false, true, true, false, false},
		{"SIGTERM today, pushed", "kill -TERM $$", 0, "sigterm", push, false, false, false, false, true, true, false, false},
		{"SIGKILL today, pushed", "kill -KILL $$", 0, "sigkill", push, false, false, false, false, true, true, false, false},
		{"SIGTERM a day ago, pushed", "kill -TERM $$", day, "", push, false, false, false, true, false, true, false, false},
		{"SIGTERM a day ago, unpushed", "kill -TERM $$", day, "sigterm", commit, false, false, false, false, true, true, false, false},
		{"gone, merged, a process works inside", "", 0, "gone", merged, false, false, true, false, true, true, false, false},
		{"exit 0, merged, a process works inside", "exit 0", 0, "exited 0", merged, false, false, true, false, true, true, false, false},
		{"forget, unpushed", "exit 3", 0, "", commit, false, true, false, true, true, true, false, false},
		{"exit 0 after ym kill today, pushed", "exit 0", 0, "exited 0", push, true, false, false, false, true, true, false, false},
		{"exit 0 after ym kill a day ago, pushed", "exit 0", day, "", push, true, false, false, true, false, true, false, false},
		{"gone after ym kill today, merged", "", 0, "gone", merged, true, false, false, false, true, true, false, false},
		{"gone, merged, a goal's agent", "", 0, "gone", merged, false, false, false, false, true, true, true, false},
		{"gone after ym kill a day ago, merged, a goal's agent", "", day, "", merged, true, false, false, true, false, false, true, false},
		{"exit 0, pushed, a goal's agent", "exit 0", 0, "", push, false, false, false, true, false, true, true, false},
		{"SIGTERM today, repository deleted", "kill -TERM $$", 0, "sigterm", commit, false, false, false, false, false, false, false, true},
		{"exit 0 after ym kill today, repository deleted", "exit 0", 0, "exited 0", dirty, true, false, false, false, false, false, false, true},
		{"gone after ym kill today, repository deleted", "", 0, "gone", commit, true, false, false, false, false, false, false, true},
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

			srv := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
			conf := filepath.Join(base, "tmux.conf")
			if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0o644); err != nil {
				t.Fatal(err)
			}
			log := filepath.Join(base, "landings.jsonl")
			l := launcher{srv: srv, records: launch.Records{Dir: filepath.Join(base, "launches")}, conf: conf, landings: log}
			rec := launch.Record{Key: "repo-wt-feature", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now().Add(-c.age),
				Worktree: launch.Worktree{Path: wt, Branch: "feature", GitDir: filepath.Join(repo, ".git")}}
			if c.goal {
				l.core, rec.Parent = goalCore(t, base, repo), "mgr-session"
			}
			if c.killed {
				rec.Killed = rec.Started
			}
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
			if c.deleted {
				for _, dir := range []string{wt, repo, filepath.Join(base, "origin.git")} {
					os.RemoveAll(dir)
				}
			}
			if c.forget {
				if _, err := l.Forget(rec.Key); err != nil {
					t.Fatal(err)
				}
			}

			rows := l.Dormant()
			kept, recorded := l.records.Find(rec.Key)
			_, statErr := os.Stat(wt)
			_, paneErr := srv.Pane(tmux.SessionName(rec.Key))
			if gone := len(rows) == 0 && paneErr != nil; gone != c.landed || (statErr == nil) != c.tree {
				t.Fatalf("rows %+v, pane err %v, worktree kept %v; want landed %v, worktree kept %v",
					rows, paneErr, statErr == nil, c.landed, c.tree)
			}
			marked := c.landed && !c.forget
			logged, _ := os.ReadFile(log)
			if recorded != !c.forget || kept.IsLanded() != marked || strings.Count(string(logged), `"key":"repo-wt-feature"`) != map[bool]int{true: 1}[marked] {
				t.Fatalf("record kept %v, marked landed %v, landing log %q; want record kept %v, marked and logged %v",
					recorded, kept.IsLanded(), logged, !c.forget, marked)
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

func TestReviveBringsBackALandedLaunchInItsOriginalWorktree(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bins := t.TempDir()
	if err := os.WriteFile(filepath.Join(bins, "claude"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	rev := func(t *testing.T, dir, ref string) string {
		out, err := exec.Command("git", "-C", dir, "rev-parse", ref).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	for _, c := range []struct {
		name    string
		target  string
		lostSHA bool
		want    string
	}{
		{"pushed, branch kept", "HEAD", false, "head"},
		{"merged, branch deleted", "HEAD:main", false, "head"},
		{"merged, branch and recorded commit gone", "HEAD:main", true, "origin/main"},
		{"repository and origin deleted", "HEAD", false, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "remote", "add", "origin", filepath.Join(base, "origin.git"))
			git(t, repo, "push", "-q", "origin", "main")
			git(t, repo, "fetch", "-q", "origin")
			wt := launch.WorktreePath(repo, "feature")
			git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
			git(t, wt, "commit", "-q", "--allow-empty", "-m", "work")
			git(t, wt, "push", "-q", "origin", c.target)
			git(t, repo, "fetch", "-q", "origin")
			head := rev(t, wt, "HEAD")

			srv := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
			conf := filepath.Join(base, "tmux.conf")
			if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0o644); err != nil {
				t.Fatal(err)
			}
			l := launcher{srv: srv, records: launch.Records{Dir: filepath.Join(base, "launches")}, conf: conf, landings: filepath.Join(base, "landings.jsonl")}
			rec := launch.Record{Key: "repo-wt-feature", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(),
				Worktree: launch.Worktree{Path: wt, Branch: "feature", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			if _, err := l.Land(rec.Key); err != nil {
				t.Fatal(err)
			}
			if c.lostSHA {
				landed, _ := l.records.Find(rec.Key)
				landed.Head = strings.Repeat("e", 40)
				l.records.Save(landed)
			}
			for _, row := range l.Launched() {
				if row.Key == rec.Key && row.Landed.IsZero() {
					t.Fatalf("landed launch listed as %+v", row)
				}
			}

			if c.want == "" {
				os.RemoveAll(repo)
				os.RemoveAll(filepath.Join(base, "origin.git"))
			}
			if _, err := l.Revive(rec.Key); err != nil {
				t.Fatal(err)
			}
			back, _ := l.records.Find(rec.Key)
			if _, st := l.state(back); c.want == "" {
				if fi, err := os.Stat(wt); err != nil || !fi.IsDir() || back.IsLanded() || st != "running" {
					t.Fatalf("directory %v, still landed %v, pane %s; want the conversation resumed at %s", err, back.IsLanded(), st, wt)
				}
				return
			}
			want := head
			if c.want != "head" {
				want = rev(t, repo, c.want)
			}
			_, st := l.state(back)
			if got := rev(t, wt, "HEAD"); got != want || back.IsLanded() || st != "running" {
				t.Fatalf("worktree at %s, still landed %v, pane %s; want %s, revived, running", got, back.IsLanded(), st, want)
			}
		})
	}
}

func TestGCDropsOnlyRecordsLandedLongAgo(t *testing.T) {
	day := 24 * time.Hour
	for _, c := range []struct {
		name   string
		landed time.Duration
		dry    bool
		pruned int
		kept   bool
	}{
		{"landed 15 days ago", 15 * day, false, 1, false},
		{"landed 15 days ago, dry run", 15 * day, true, 1, true},
		{"landed 13 days ago", 13 * day, false, 0, true},
		{"never landed", 0, false, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: t.TempDir()}}
			rec := launch.Record{Key: "app-wt-x", Harness: "claude", Started: time.Now().Add(-30 * day)}
			if c.landed > 0 {
				rec.Landed = time.Now().Add(-c.landed)
			}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			_, pruned := l.gc(nil, c.dry, false)
			if _, kept := l.records.Find(rec.Key); pruned != c.pruned || kept != c.kept {
				t.Fatalf("pruned %d, record kept %v; want %d, %v", pruned, kept, c.pruned, c.kept)
			}
		})
	}
}

// goalCore is a ym with one active goal in repo whose manager runs as mgr-session.
func goalCore(t *testing.T, dir, repo string) *core.Core {
	t.Helper()
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &core.Core{Store: st, StateDir: dir, Procs: func() ([]proc.Proc, error) { return nil, nil }}
	_, m, err := c.NewGoal("ship the importer", repo, "")
	if err != nil {
		t.Fatal(err)
	}
	m.Session = "mgr-session"
	if err := st.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	return c
}
