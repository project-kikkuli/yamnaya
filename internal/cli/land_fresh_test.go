package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestASessionThatEndsRightAfterItsLaunchIsCleanedUpButNotOneStillRunning(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name   string
		agent  string
		landed bool
	}{
		{"killed by a signal", "kill -9 $$", true},
		{"exited cleanly", "exit 0", true},
		{"still running", "sleep 60", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "remote", "add", "origin", filepath.Join(base, "origin.git"))
			git(t, repo, "push", "-q", "origin", "main")
			wt := launch.WorktreePath(repo, "task")
			git(t, repo, "worktree", "add", "-q", "-b", "task", wt, "origin/main")
			socket := tmuxtest.Socket(t)
			conf := filepath.Join(base, "tmux.conf")
			os.WriteFile(conf, []byte(tmux.ServerConf), 0o644)
			srv := &tmux.Host{Socket: socket, Kind: tmux.Server}
			l := launcher{srv: srv, records: launch.Records{Dir: filepath.Join(base, "launches")}, conf: conf, landings: filepath.Join(base, "landings.jsonl")}
			rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(),
				Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			starter := &tmux.Host{Socket: socket, Kind: tmux.Server}
			if _, err := starter.Start(conf, "keep", base, []string{"sleep", "60"}); err != nil {
				t.Fatal(err)
			}
			srv.Panes()
			name := tmux.SessionName(rec.Key)
			if _, err := starter.Start(conf, name, wt, []string{"sh", "-c", c.agent}); err != nil {
				t.Fatal(err)
			}
			for end := time.Now().Add(5 * time.Second); c.landed; time.Sleep(10 * time.Millisecond) {
				if p, err := starter.Pane(name); err == nil && p.Dead {
					break
				}
				if time.Now().After(end) {
					t.Fatal("the agent never ended")
				}
			}
			l.Sweep(map[string]string{rec.Key: "gone"})
			l.srv = &tmux.Host{Socket: socket, Kind: tmux.Server}
			rows := l.Dormant()
			kept, _ := l.records.Find(rec.Key)
			if kept.IsLanded() != c.landed {
				t.Fatalf("landed %v, want %v; rows %+v", kept.IsLanded(), c.landed, rows)
			}
			if _, err := os.Stat(wt); (err == nil) == c.landed || len(rows) != 0 {
				t.Fatalf("worktree kept %v, rows %+v; want it removed only once it ended, and no row either way", err == nil, rows)
			}
		})
	}
}
