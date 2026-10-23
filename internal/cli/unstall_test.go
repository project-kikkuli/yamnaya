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
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestASessionLeftAtTheTrustDialogIsTrustedAndRestarted(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name    string
		trusted bool
		age     time.Duration
		starts  int
	}{
		{"trust lost before it started", false, time.Minute, 2},
		{"trusted", true, time.Minute, 1},
		{"long past its start", false, time.Hour, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			wt := launch.WorktreePath(repo, "task")
			git(t, repo, "worktree", "add", "-q", "-b", "task", wt)
			config := filepath.Join(base, "config")
			os.MkdirAll(config, 0o755)
			t.Setenv("CLAUDE_CONFIG_DIR", config)
			if c.trusted {
				if err := launch.TrustClaudeDir(wt); err != nil {
					t.Fatal(err)
				}
			}
			starts := filepath.Join(base, "starts")
			fake := filepath.Join(base, "claude")
			script := fmt.Sprintf("#!/bin/sh\necho start >> %q\nif grep -q %q %q/.claude.json 2>/dev/null; then echo ready; else echo %q; fi\nexec sleep 60\n", starts, wt, config, trustPrompt)
			if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YAMNAYA_CLAUDE_BIN", fake)

			srv := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
			conf := filepath.Join(base, "tmux.conf")
			os.WriteFile(conf, []byte(tmux.ServerConf), 0o644)
			l := launcher{srv: srv, records: launch.Records{Dir: filepath.Join(base, "launches")}, conf: conf}
			rec := launch.Record{Key: "repo-wt-task", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now().Add(-c.age),
				Worktree: launch.Worktree{Path: wt, Branch: "task", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			argv, err := startArgv(rec)
			if err != nil {
				t.Fatal(err)
			}
			name := tmux.SessionName(rec.Key)
			if _, err := srv.Start(conf, name, wt, argv); err != nil {
				t.Fatal(err)
			}
			waitScreen(t, srv, name, map[bool]string{true: "ready", false: trustPrompt}[c.trusted])
			if err := l.unstall(time.Now()); err != nil {
				t.Fatal(err)
			}
			if c.starts == 2 {
				waitScreen(t, srv, name, "ready")
			}
			if got, _ := os.ReadFile(starts); strings.Count(string(got), "start") != c.starts {
				t.Fatalf("claude started %d times, want %d", strings.Count(string(got), "start"), c.starts)
			}
		})
	}
}

func waitScreen(t *testing.T, srv *tmux.Host, name, want string) {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if s, err := srv.Capture(name); err == nil && strings.Contains(s, want) {
			return
		}
		if time.Now().After(end) {
			s, _ := srv.Capture(name)
			t.Fatalf("pane never showed %q:\n%s", want, s)
		}
	}
}
