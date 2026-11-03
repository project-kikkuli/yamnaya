package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// closeWorld is a goal under a chief on a real store; caller is the harness session the command runs in, "" for a plain shell.
func closeWorld(t *testing.T, caller string) (*core.Core, core.Goal, core.Manager, core.Manager) {
	t.Helper()
	if caller != "" {
		t.Setenv("CODEX_THREAD_ID", caller)
	} else {
		t.Setenv("CODEX_THREAD_ID", "")
		t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	}
	st, err := core.OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	c := &core.Core{Store: st, StateDir: t.TempDir(), Procs: func() ([]proc.Proc, error) {
		return []proc.Proc{{PID: os.Getpid(), PPID: 1, Args: []string{"sh"}}}, nil
	}}
	g, err := st.AddGoal(core.Goal{Ask: "implement CSV", State: core.GoalActive})
	if err != nil {
		t.Fatal(err)
	}
	chief, err := st.AddManager(core.Manager{Role: core.RoleChief, Name: "boss", Session: "chief-session"})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveManager(chief); err != nil {
		t.Fatal(err)
	}
	m, err := st.AddManager(core.Manager{Role: core.RoleManager, GoalID: g.ID, Session: "manager-session", ParentID: chief.ID})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.SaveManager(m); err != nil {
		t.Fatal(err)
	}
	g.ManagerID = m.ID
	if err = st.SaveGoal(g); err != nil {
		t.Fatal(err)
	}
	return c, g, m, chief
}

func runGoal(c *core.Core, args ...string) (string, error) {
	with := func(run runFunc) func(*cobra.Command, []string) error {
		return func(cmd *cobra.Command, args []string) error { return run(&env{core: c}, cmd, args) }
	}
	cmd := goalCmd(with, with)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	return out.String(), err
}

func TestGoalCloseKnowsWhoIsCalling(t *testing.T) {
	for _, tc := range []struct {
		name, caller, refused, actor string
	}{
		{name: "a plain shell is the owner", actor: "the owner"},
		{name: "the chief above", caller: "chief-session", actor: "chief "},
		{name: "the goal's manager", caller: "manager-session", refused: "ym goal done"},
		{name: "any other session", caller: "worker-session", refused: "not a chief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, g, _, _ := closeWorld(t, tc.caller)
			out, err := runGoal(c, "close", g.ID, "--as", "abandoned", "--why", "not needed")
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("err %v, out %q; want refused naming %q", err, out, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			shown, err := runGoal(c, "show", g.ID)
			if err != nil {
				t.Fatalf("show of a closed goal: %v", err)
			}
			if !strings.Contains(shown, "closed as abandoned by "+tc.actor) || !strings.Contains(shown, "not needed") {
				t.Fatalf("show printed %q", shown)
			}
			listed, err := runGoal(c, "list", "--archived", "--json")
			if err != nil {
				t.Fatal(err)
			}
			var goals []core.ArchivedGoal
			if err := json.Unmarshal([]byte(listed), &goals); err != nil {
				t.Fatal(err)
			}
			if len(goals) != 1 || goals[0].ID != g.ID || goals[0].Disposition != "abandoned" || goals[0].Reason != "not needed" || !strings.HasPrefix(goals[0].Actor, tc.actor) {
				t.Fatalf("archived %+v", goals)
			}
			if tree, err := runGoal(c, "list"); err != nil || strings.Contains(tree, "implement CSV") {
				t.Fatalf("the tree still lists it: %q (%v)", tree, err)
			}
		})
	}
}

func TestClosingAGoalKeepsUnpushedWorkAndRevivingPutsItBack(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	bins := t.TempDir()
	if err := os.WriteFile(filepath.Join(bins, "claude"), []byte("#!/bin/sh\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	for _, as := range []string{core.CloseSuperseded, core.CloseAbandoned} {
		t.Run(as, func(t *testing.T) {
			c, g, m, _ := closeWorld(t, "")
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
			git(t, wt, "commit", "-q", "--allow-empty", "-m", "unpushed work")
			head := strings.TrimSpace(gitOut(t, wt, "rev-parse", "HEAD"))
			if err := os.WriteFile(filepath.Join(wt, "notes.txt"), []byte("half done"), 0o644); err != nil {
				t.Fatal(err)
			}
			srv := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
			conf := filepath.Join(base, "tmux.conf")
			if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0o644); err != nil {
				t.Fatal(err)
			}
			l := launcher{core: c, srv: srv, records: launch.Records{Dir: filepath.Join(base, "launches")}, conf: conf, landings: filepath.Join(base, "landings.jsonl")}
			rec := launch.Record{Key: "repo-wt-feature", Harness: "claude", Session: "manager-session", Prompt: "fix it", Started: time.Now(),
				Worktree: launch.Worktree{Path: wt, Branch: "feature", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			c.Launches = l
			b, err := c.Board()
			if err != nil {
				t.Fatal(err)
			}
			closed, err := c.CloseGoal(b, core.CloseOrder{Ref: g.ID, As: as, Why: "taken over"})
			if err != nil {
				t.Fatal(err)
			}
			if closed.Toll.Commits != 1 || closed.Toll.Files != 1 {
				t.Fatalf("toll %+v, want the unpushed commit and uncommitted file counted", closed.Toll)
			}
			if _, err := os.Stat(wt); err == nil {
				t.Fatalf("close left the worktree %s", wt)
			}
			landed, found := l.records.Find(rec.Key)
			if !found || landed.Recovery.Bundle == "" || landed.Goal != m.ID {
				t.Fatalf("record %+v (%v), want the work bundled under %s", landed, found, m.ID)
			}
			msg, err := l.Revive(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(msg, "1 sessions") {
				t.Fatalf("revive said %q", msg)
			}
			if got := strings.TrimSpace(gitOut(t, wt, "rev-parse", "HEAD")); got != head {
				t.Fatalf("worktree at %s, want %s", got, head)
			}
			if notes, _ := os.ReadFile(filepath.Join(wt, "notes.txt")); string(notes) != "half done" {
				t.Fatalf("uncommitted notes.txt came back as %q", notes)
			}
			if now, _ := c.Store.Goal(g.ID); now.State != core.GoalActive {
				t.Fatalf("goal %s after revive", now.State)
			}
		})
	}
}

func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
