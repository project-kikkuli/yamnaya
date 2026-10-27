package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestALaunchIsNeverLandedOrRevivedWhileTmuxCannotListItsPane(t *testing.T) {
	real, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	for _, agent := range []string{"exit 0", "sleep 60"} {
		t.Run(agent, func(t *testing.T) {
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "init", "-q", "-b", "main", "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "remote", "add", "origin", filepath.Join(base, "origin.git"))
			git(t, repo, "push", "-q", "origin", "main")
			wt := launch.WorktreePath(repo, "task")
			git(t, repo, "worktree", "add", "-q", "-b", "task", wt, "origin/main")

			bin := filepath.Join(base, "bin")
			fail := filepath.Join(base, "fail")
			os.MkdirAll(bin, 0o755)
			wrapper := "#!/bin/sh\nfor a in \"$@\"; do if [ \"$a\" = list-panes ] && [ -e " + fail + " ]; then echo 'server exited unexpectedly' >&2; exit 1; fi; done\nexec " + real + " \"$@\"\n"
			if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(wrapper), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

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
			name := tmux.SessionName(rec.Key)
			if _, err := srv.Start(conf, name, wt, []string{"sh", "-c", agent}); err != nil {
				t.Fatal(err)
			}
			if agent == "exit 0" {
				for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
					if p, err := srv.Pane(name); err == nil && p.Dead {
						break
					}
					if time.Now().After(end) {
						t.Fatal("the agent never ended")
					}
				}
			}

			os.WriteFile(fail, nil, 0o644)
			l.Sweep(core.Stopped(l.Launched()))
			rows := l.Dormant()
			_, reviveErr := l.revive(rec)
			kept, _ := l.records.Find(rec.Key)
			_, statErr := os.Stat(wt)
			if kept.IsLanded() || statErr != nil || len(rows) != 0 || reviveErr == nil {
				t.Fatalf("while tmux fails: landed %v, worktree err %v, rows %+v, revive err %v; want nothing landed, listed or revived",
					kept.IsLanded(), statErr, rows, reviveErr)
			}
			for _, launched := range l.Launched() {
				if launched.Key == rec.Key && !launched.MayRun() {
					t.Fatalf("launched state %q while tmux fails; want it read as possibly running", launched.State)
				}
			}

			os.Remove(fail)
			time.Sleep(600 * time.Millisecond)
			l.Sweep(core.Stopped(l.Launched()))
			kept, _ = l.records.Find(rec.Key)
			if want := agent == "exit 0"; kept.IsLanded() != want {
				t.Fatalf("once tmux answers, landed %v, want %v", kept.IsLanded(), want)
			}
		})
	}
}
