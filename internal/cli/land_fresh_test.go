package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestASessionKilledRightAfterItsLaunchStaysForItsManager(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name   string
		agent  string
		landed bool
		state  string
	}{
		{"killed by a signal", "kill -9 $$", false, "sigkill"},
		{"exited cleanly", "exit 0", true, ""},
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
			socket := fmt.Sprintf("ymtest-%d-%d", os.Getpid(), time.Now().UnixNano())
			t.Cleanup(func() { exec.Command("tmux", "-L", socket, "kill-server").Run() })
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
			for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
				if p, err := starter.Pane(name); err == nil && p.Dead {
					break
				}
				if time.Now().After(end) {
					t.Fatal("the agent never ended")
				}
			}
			rows := l.Dormant()
			kept, _ := l.records.Find(rec.Key)
			if kept.IsLanded() != c.landed {
				t.Fatalf("landed %v, want %v; rows %+v", kept.IsLanded(), c.landed, rows)
			}
			if !c.landed && (len(rows) != 1 || rows[0].State != c.state) {
				t.Fatalf("rows %+v, want one row in state %q", rows, c.state)
			}
		})
	}
}
