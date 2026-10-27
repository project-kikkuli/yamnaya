package cli

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
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
	t.Setenv("YAMNAYA_YM_SOCKET", "ymtest")
	src := t.TempDir()
	gitDir, admin := filepath.Join(src, "app", ".git"), filepath.Join(src, "app", ".git", "worktrees", "app-wt-x")
	wt := launch.Worktree{Path: filepath.Join(src, "app-wt-x"), Branch: "x", GitDir: gitDir}
	if err := os.MkdirAll(wt.Path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt.Path, ".git"), []byte("gitdir: "+admin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	resume := func(prompt string) func(launch.Record) ([]string, error) {
		return func(r launch.Record) ([]string, error) { return resumeArgv(r, prompt) }
	}
	commits := []string{filepath.Join(gitDir, "objects"), filepath.Join(gitDir, "refs"), filepath.Join(gitDir, "logs"), admin}
	for _, c := range []struct {
		name   string
		rec    launch.Record
		argv   func(launch.Record) ([]string, error)
		writes bool
	}{
		{"codex start", launch.Record{Harness: "codex", Prompt: "go", Worktree: wt}, startArgv, true},
		{"codex resume", launch.Record{Harness: "codex", Session: "t1", Worktree: wt}, resume(""), true},
		{"codex resume with a message", launch.Record{Harness: "codex", Session: "t1", Prompt: "carry on", Worktree: wt}, resume("carry on"), true},
		{"claude start", launch.Record{Harness: "claude", Session: "s1", Prompt: "go", Worktree: wt}, startArgv, false},
		{"claude resume", launch.Record{Harness: "claude", Session: "s1", Worktree: wt}, resume(""), false},
		{"claude resume with a message", launch.Record{Harness: "claude", Session: "s1", Prompt: "carry on", Worktree: wt}, resume("carry on"), false},
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
			passes := slices.Contains(argv, "shell_environment_policy.set.YAMNAYA_STATE_DIR="+strconv.Quote(state)) &&
				slices.Contains(argv, "shell_environment_policy.set.YAMNAYA_YM_SOCKET="+strconv.Quote("ymtest"))
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
			if last := argv[len(argv)-1]; last != c.rec.Prompt && last != c.rec.Prompt+slotNote && last != c.rec.Session {
				t.Fatalf("argv %q must end with the prompt or session", argv)
			}
			if strings.HasSuffix(c.name, "start") && !strings.HasSuffix(argv[len(argv)-1], slotNote) {
				t.Fatalf("argv %q starts an agent without telling it to run heavy commands through ym slot", argv)
			}
		})
	}
}

func TestEnvModelPinsSessionsOfItsHarnessOnly(t *testing.T) {
	bins := t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bins, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bins)
	t.Setenv("YAMNAYA_STATE_DIR", t.TempDir())
	wt := launch.Worktree{Path: t.TempDir()}
	for _, c := range []struct {
		name    string
		env     map[string]string
		rec     launch.Record
		resume  bool
		want    string
		wantArg bool
	}{
		{"claude start pinned", map[string]string{"YAMNAYA_CLAUDE_MODEL": "claude-haiku-5-5"}, launch.Record{Harness: "claude", Session: "s", Prompt: "go", Worktree: wt}, false, "claude-haiku-5-5", true},
		{"claude resume pinned", map[string]string{"YAMNAYA_CLAUDE_MODEL": "claude-haiku-5-5"}, launch.Record{Harness: "claude", Session: "s", Worktree: wt}, true, "claude-haiku-5-5", true},
		{"record model beats env on resume", map[string]string{"YAMNAYA_CLAUDE_MODEL": "claude-haiku-5-5"}, launch.Record{Harness: "claude", Session: "s", Model: "claude-opus-5-5", Worktree: wt}, true, "claude-opus-5-5", true},
		{"codex start pinned", map[string]string{"YAMNAYA_CODEX_MODEL": "gpt-6-luna"}, launch.Record{Harness: "codex", Prompt: "go", Worktree: wt}, false, "gpt-6-luna", true},
		{"claude env does not touch codex", map[string]string{"YAMNAYA_CLAUDE_MODEL": "claude-haiku-5-5"}, launch.Record{Harness: "codex", Prompt: "go", Worktree: wt}, false, "", false},
		{"unset env keeps the default", nil, launch.Record{Harness: "claude", Session: "s", Prompt: "go", Worktree: wt}, false, "", false},
		{"unset env keeps the default on resume", nil, launch.Record{Harness: "claude", Session: "s", Worktree: wt}, true, "", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Setenv("YAMNAYA_CLAUDE_MODEL", "")
			t.Setenv("YAMNAYA_CODEX_MODEL", "")
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			argv, err := startArgv(c.rec)
			if c.resume {
				argv, err = resumeArgv(c.rec, "")
			}
			if err != nil {
				t.Fatal(err)
			}
			i := slices.Index(argv, "--model")
			if (i >= 0) != c.wantArg || (i >= 0 && argv[i+1] != c.want) {
				t.Fatalf("argv %q: --model want %q (present %v)", argv, c.want, c.wantArg)
			}
		})
	}
}
