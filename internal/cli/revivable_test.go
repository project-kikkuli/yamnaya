package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

// fakeAgent puts a claude on PATH that writes where it started and with what arguments to the file it returns.
func fakeAgent(t *testing.T) string {
	t.Helper()
	bins, ran := t.TempDir(), filepath.Join(t.TempDir(), "ran")
	script := fmt.Sprintf("#!/bin/sh\necho \"$PWD $*\" > %s.tmp && mv %s.tmp %s\nexec sleep 30\n", ran, ran, ran)
	if err := os.WriteFile(filepath.Join(bins, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	return ran
}

func testLauncher(t *testing.T, base string) launcher {
	t.Helper()
	srv := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
	conf := filepath.Join(base, "tmux.conf")
	if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0o644); err != nil {
		t.Fatal(err)
	}
	return launcher{srv: srv, records: launch.Records{Dir: filepath.Join(base, "launches")}, conf: conf, landings: filepath.Join(base, "landings.jsonl")}
}

// resumed waits for the fake agent's line and returns it.
func resumed(t *testing.T, ran string) string {
	t.Helper()
	for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if b, err := os.ReadFile(ran); err == nil && len(b) > 0 {
			return strings.TrimSpace(string(b))
		}
	}
	t.Fatal("the revived agent never started")
	return ""
}

func TestALaunchWhoseRepoIsDeletedStaysDormantAndRevivesItsConversation(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name     string
		dropRepo bool
		landed   bool
		lost     string
	}{
		{"worktree and repo deleted", true, true, launch.RepoGone},
		{"only the worktree deleted", false, true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			ran := fakeAgent(t)
			base, _ := filepath.EvalSymlinks(t.TempDir())
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "push", "-q", "origin", "main")
			wt := launch.WorktreePath(repo, "feature")
			git(t, repo, "worktree", "add", "-q", "-b", "feature", wt)
			git(t, wt, "commit", "-q", "--allow-empty", "-m", "work")
			git(t, wt, "push", "-q", "origin", "feature")
			l := testLauncher(t, base)
			rec := launch.Record{Key: "repo-wt-feature", Harness: "claude", Session: "s1", Prompt: "fix it", Started: time.Now(),
				Head: launch.Head(wt), Worktree: launch.Worktree{Path: wt, Branch: "feature", GitDir: filepath.Join(repo, ".git")}}
			if err := l.records.Save(rec); err != nil {
				t.Fatal(err)
			}
			os.RemoveAll(wt)
			if c.dropRepo {
				os.RemoveAll(repo)
			}

			l.Sweep(core.Stopped(l.Launched()))
			rows := l.Dormant()
			kept, _ := l.records.Find(rec.Key)
			if kept.IsLanded() != c.landed || (len(rows) == 1) == c.landed {
				t.Fatalf("rows %+v, landed %v; want landed %v", rows, kept.IsLanded(), c.landed)
			}
			if !c.landed && rows[0].State != c.lost {
				t.Fatalf("row state %q, want %q", rows[0].State, c.lost)
			}
			l.gc(nil, false, false)
			if _, ok := l.records.Find(rec.Key); !ok {
				t.Fatal("gc dropped the record")
			}

			msg, err := l.Revive(rec.Key)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := resumed(t, ran), wt+" --dangerously-skip-permissions --resume s1"; got != want {
				t.Fatalf("agent ran as %q, want %q", got, want)
			}
			if env := regexp.MustCompile(`\b[A-Z][A-Z0-9_]*=\S*`).FindString(msg); env != "" || strings.Contains(msg, "--resume") || !strings.Contains(msg, " in "+wt+";") || !strings.HasSuffix(msg, "; open: ym open "+rec.Key) {
				t.Fatalf("revive said %q; want what was revived, where and how to open it, with no command line or %q", msg, env)
			}
			if strings.Contains(msg, launch.RepoGone) != (c.lost != "") {
				t.Fatalf("revive said %q; want it to say %q: %v", msg, launch.RepoGone, c.lost != "")
			}
			if hasCode := launch.Head(wt) == rec.Head; hasCode == (c.lost != "") {
				t.Fatalf("worktree at %q, recorded %q; want code back %v", launch.Head(wt), rec.Head, c.lost == "")
			}
		})
	}
}

func TestASessionYmNeverRanIsRevivableOnceItEnds(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name     string
		lastSeen time.Duration
		dropDir  bool
		state    string
		landed   bool
	}{
		{"ended just now", 0, false, "ended outside ym", false},
		{"ended just now, its directory deleted", 0, true, launch.DirGone, false},
		{"last seen over a day ago", 25 * time.Hour, false, "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ran := fakeAgent(t)
			base, _ := filepath.EvalSymlinks(t.TempDir())
			dir := filepath.Join(base, "notes")
			os.MkdirAll(dir, 0o755)
			l := testLauncher(t, base)
			s := core.Live{Session: core.Session{Harness: "claude", ID: "s2", Cwd: dir, PID: 4242, Title: "jot notes"}}
			l.Saw([]core.Live{s, {Session: core.Session{Harness: "claude", ID: "bg", Cwd: dir, Background: true}}})
			if all, _ := l.records.All(); len(all) != 1 {
				t.Fatalf("records %+v; want one for the terminal session", all)
			}
			rec, _ := l.records.Find("s2")
			rec.Seen = rec.Seen.Add(-c.lastSeen)
			l.records.Save(rec)
			if c.dropDir {
				os.RemoveAll(dir)
			}

			l.Sweep(core.Stopped(l.Launched()))
			rows := l.Dormant()
			rec, _ = l.records.Find("s2")
			if rec.IsLanded() != c.landed || (len(rows) == 1) == c.landed || (!c.landed && rows[0].State != c.state) {
				t.Fatalf("rows %+v, landed %v; want state %q, landed %v", rows, rec.IsLanded(), c.state, c.landed)
			}
			for _, r := range l.Launched() {
				if r.Session == "s2" && r.Landed.IsZero() {
					t.Fatalf("launched lists %+v, which ym never launched, as a launch", r)
				}
			}
			if c.landed {
				l.Saw([]core.Live{s})
				if back, _ := l.records.Find("s2"); back.IsLanded() || len(l.Dormant()) != 1 {
					t.Fatalf("seen running again, record %+v; want it dormant again", back)
				}
			}

			if _, err := l.Revive("s2"); err != nil {
				t.Fatal(err)
			}
			if got, want := resumed(t, ran), dir+" --dangerously-skip-permissions --resume s2"; got != want {
				t.Fatalf("agent ran as %q, want %q", got, want)
			}
			if back, _ := l.records.Find("s2"); !back.Seen.IsZero() {
				t.Fatalf("revived record %+v still marks it as run outside ym", back)
			}
		})
	}
}
