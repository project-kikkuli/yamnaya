package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/harness/codex"
	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestAgentCommandsBypassPermissionsAndPreserveContext(t *testing.T) {
	bins, state := t.TempDir(), t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bins, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bins)
	t.Setenv("YAMNAYA_STATE_DIR", state)
	src := t.TempDir()
	gitDir, admin := filepath.Join(src, "app", ".git"), filepath.Join(src, "app", ".git", "worktrees", "app-wt-x")
	wt := launch.Worktree{Path: filepath.Join(src, "app-wt-x"), Branch: "x", GitDir: gitDir}
	if err := os.MkdirAll(wt.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	commits := []string{filepath.Join(gitDir, "objects"), filepath.Join(gitDir, "refs"), filepath.Join(gitDir, "logs"), admin}
	for _, c := range []struct {
		name   string
		rec    launch.Record
		argv   func(launch.Record) ([]string, error)
		writes bool
	}{
		{"codex start", launch.Record{Harness: "codex", Prompt: "go", Worktree: wt}, startArgv, true},
		{"codex resume", launch.Record{Harness: "codex", Session: "t1", Worktree: wt}, resumeArgv, true},
		{"claude start", launch.Record{Harness: "claude", Session: "s1", Prompt: "go", Worktree: wt}, startArgv, false},
		{"claude resume", launch.Record{Harness: "claude", Session: "s1", Worktree: wt}, resumeArgv, false},
		{"claude resume command", launch.Record{Harness: "claude", Session: "s1"}, func(r launch.Record) ([]string, error) {
			return claude.New().Resume(r.Session), nil
		}, false},
		{"codex resume command", launch.Record{Harness: "codex", Session: "t1"}, func(r launch.Record) ([]string, error) {
			return codex.New().Resume(r.Session), nil
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			argv, err := c.argv(c.rec)
			if err != nil {
				t.Fatal(err)
			}
			flag := map[string]string{"claude": "--dangerously-skip-permissions", "codex": "--yolo"}[c.rec.Harness]
			if !slices.Contains(argv, flag) {
				t.Fatalf("argv %q must bypass permission prompts with %s", argv, flag)
			}
			dirs := map[string]bool{}
			for i, a := range argv {
				if a == "--add-dir" && i+1 < len(argv) {
					dirs[argv[i+1]] = true
				}
			}
			passes := slices.Contains(argv, "shell_environment_policy.set.YAMNAYA_STATE_DIR="+strconv.Quote(state))
			got := dirs[state] && passes
			for _, d := range commits {
				got = got && dirs[d]
			}
			if got != c.writes {
				t.Fatalf("argv %q: writable state and commit dirs, state dir in its shells = %v, want %v", argv, got, c.writes)
			}
			if dirs[gitDir] || dirs[filepath.Join(gitDir, "hooks")] {
				t.Fatalf("argv %q opens the git dir's hooks and config to the sandbox", argv)
			}
			if last := argv[len(argv)-1]; last != c.rec.Prompt && last != c.rec.Session {
				t.Fatalf("argv %q must end with the prompt or session", argv)
			}
		})
	}
}
