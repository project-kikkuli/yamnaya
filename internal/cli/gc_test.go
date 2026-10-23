package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestGCRemovesOnlyLinkedWorktreesItsStateDirMayTouchAndLogsEachRemoval(t *testing.T) {
	for _, c := range []struct {
		name       string
		everywhere bool
		gone       []string
	}{
		{"another state dir", false, []string{"mine"}},
		{"the default state dir", true, []string{"adopted", "mine", "theirs"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			old := time.Now().Add(-launch.Idle - time.Hour)
			t.Setenv("GIT_COMMITTER_DATE", fmt.Sprintf("@%d +0000", old.Unix()))
			base, _ := filepath.EvalSymlinks(t.TempDir())
			home, plain := filepath.Join(base, "home"), filepath.Join(base, "plain")
			os.MkdirAll(home, 0o755)
			os.MkdirAll(plain, 0o755)
			t.Setenv("HOME", home)
			git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
			git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
			repo := filepath.Join(base, "repo")
			git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
			git(t, repo, "push", "-q", "origin", "main")
			git(t, repo, "fetch", "-q", "origin")
			trees := map[string]string{}
			for _, b := range []string{"adopted", "mine", "theirs"} {
				trees[b] = launch.WorktreePath(repo, b)
				git(t, repo, "worktree", "add", "-q", "-b", b, trees[b])
				admin := filepath.Join(repo, ".git", "worktrees", filepath.Base(trees[b]))
				for _, p := range []string{trees[b], filepath.Join(admin, "HEAD")} {
					os.Chtimes(p, old, old)
				}
			}
			state := t.TempDir()
			l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: filepath.Join(state, "launches")},
				removals: filepath.Join(state, "gc.log"), everywhere: c.everywhere}
			head := launch.Head(trees["mine"])
			rec := launch.Record{Key: "repo-wt-mine", Harness: "claude", Session: "s1", Started: old, Head: head,
				Worktree: launch.Worktree{Path: trees["mine"], Branch: "mine", GitDir: filepath.Join(repo, ".git")}}
			gitDir := filepath.Join(repo, ".git")
			for _, r := range []launch.Record{rec,
				{Key: "adopt-wt", Adopted: true, Worktree: launch.Worktree{Path: trees["adopted"], Branch: "adopted", GitDir: gitDir}},
				{Key: "adopt-home", Adopted: true, Worktree: launch.Worktree{Path: home}},
				{Key: "adopt-main", Adopted: true, Worktree: launch.Worktree{Path: repo, Branch: "main", GitDir: gitDir}},
				{Key: "adopt-plain", Adopted: true, Worktree: launch.Worktree{Path: plain}},
			} {
				if err := l.records.Save(r); err != nil {
					t.Fatal(err)
				}
			}

			all, _ := l.gc([]string{repo}, false, false)
			for _, s := range all {
				if len(s.Errors) > 0 {
					t.Fatal(s.Errors)
				}
			}
			for b, wt := range trees {
				if _, err := os.Stat(wt); (err != nil) != slices.Contains(c.gone, b) {
					t.Fatalf("%s worktree on disk %v, want gone %v", b, err == nil, slices.Contains(c.gone, b))
				}
				has := exec.Command("git", "-C", repo, "rev-parse", "--verify", "--quiet", "refs/heads/"+b).Run() == nil
				if has == slices.Contains(c.gone, b) {
					t.Fatalf("%s branch kept %v, want gone %v", b, has, slices.Contains(c.gone, b))
				}
			}
			for _, dir := range []string{home, plain, repo} {
				if _, err := os.Stat(dir); err != nil {
					t.Fatalf("gc removed %s", dir)
				}
			}
			b, err := os.ReadFile(l.removals)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(lines) != 2*len(c.gone) {
				t.Fatalf("logged %q, want a worktree and a branch line for each of %v", lines, c.gone)
			}
			at, _, _ := strings.Cut(lines[0], " ")
			if _, err := time.Parse(time.RFC3339, at); err != nil {
				t.Fatalf("line %q starts with no RFC3339 time: %v", lines[0], err)
			}
			want := fmt.Sprintf(`removed worktree repo=%q path=%q branch="mine" head=%q reason="clean, every commit on a remote`, repo, trees["mine"], head)
			if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) }) {
				t.Fatalf("logged %q, want a line with %s", lines, want)
			}
			want = fmt.Sprintf(`deleted branch repo=%q path="" branch="mine" head=%q reason="merged into origin/main"`, repo, head)
			if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, want) }) {
				t.Fatalf("logged %q, want a line with %s", lines, want)
			}
		})
	}
}

func TestOnlyTheDefaultStateDirSweepsEveryRepository(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	def := filepath.Join(home, ".local", "state", "yamnaya")
	os.MkdirAll(def, 0o755)
	os.Symlink(def, filepath.Join(home, "linked"))
	for _, c := range []struct {
		dir  string
		want bool
	}{
		{def, true},
		{filepath.Join(home, "linked"), true},
		{t.TempDir(), false},
		{filepath.Join(home, "missing"), false},
	} {
		if got := defaultStateDir(c.dir); got != c.want {
			t.Errorf("defaultStateDir(%s) = %v, want %v", c.dir, got, c.want)
		}
	}
}

func TestYmNeverAdoptsASessionInTheHomeDirectoryOrAnotherYmsRehearsal(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(base, "home")
	repo, plain := filepath.Join(home, "repo"), filepath.Join(base, "plain")
	rehearsals := filepath.Join(base, "rehearsals")
	run := filepath.Join(rehearsals, "run", "proving-ground-wt-x")
	for _, d := range []string{repo, plain, run} {
		os.MkdirAll(d, 0o755)
	}
	git(t, repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(rehearsals, core.RehearsalMark), nil, 0o644)
	t.Setenv("HOME", home)
	owner := filepath.Join(base, "state")
	for _, c := range []struct {
		name    string
		cwd     string
		state   string
		adopted bool
		why     string
	}{
		{"the home directory", home, owner, false, "home directory"},
		{"the home directory through a trailing slash", home + "/", owner, false, "home directory"},
		{"a repository in the home directory", repo, owner, true, ""},
		{"a plain directory elsewhere", plain, owner, true, ""},
		{"a rehearsal, seen by the owner's ym", run, owner, false, "rehearsal"},
		{"a rehearsal, seen by the ym running it", run, filepath.Join(rehearsals, "run", "state"), true, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("YAMNAYA_STATE_DIR", c.state)
			l := launcher{srv: &tmux.Host{Socket: "ymtest-unused", Kind: tmux.Server}, records: launch.Records{Dir: t.TempDir()}}
			s := core.Live{Session: core.Session{Harness: "claude", ID: "s1", Cwd: c.cwd, PID: 4242}}
			l.Saw([]core.Live{s})
			if _, ok := l.records.Find("s1"); ok != c.adopted {
				t.Fatalf("recorded %v, want %v", ok, c.adopted)
			}
			if !c.adopted {
				if _, err := l.Adopt(s, nil); err == nil || !strings.Contains(err.Error(), c.why) {
					t.Fatalf("Adopt = %v, want a refusal naming the %s", err, c.why)
				}
				if _, err := l.takeOver(s, "codex", "s2"); err == nil {
					t.Fatalf("takeOver recorded %s", c.cwd)
				}
			}
		})
	}
}
