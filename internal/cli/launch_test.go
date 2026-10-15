package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/launch"
)

func TestLaunchedCodexCanWriteYmStateAndCommit(t *testing.T) {
	bins, state := t.TempDir(), t.TempDir()
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bins, name), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bins)
	t.Setenv("YAMNAYA_STATE_DIR", state)
	wt := launch.Worktree{Path: "/src/app-wt-x", Branch: "x", GitDir: "/src/app/.git"}
	for _, c := range []struct {
		name   string
		rec    launch.Record
		argv   func(launch.Record) ([]string, error)
		writes bool
	}{
		{"codex start", launch.Record{Harness: "codex", Prompt: "go", Worktree: wt}, startArgv, true},
		{"codex resume", launch.Record{Harness: "codex", Session: "t1", Worktree: wt}, resumeArgv, true},
		{"claude start", launch.Record{Harness: "claude", Session: "s1", Prompt: "go", Worktree: wt}, startArgv, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			argv, err := c.argv(c.rec)
			if err != nil {
				t.Fatal(err)
			}
			dirs := map[string]bool{}
			for i, a := range argv {
				if a == "--add-dir" && i+1 < len(argv) {
					dirs[argv[i+1]] = true
				}
			}
			if got := dirs[state] && dirs["/src/app/.git"]; got != c.writes {
				t.Fatalf("argv %q: writable state and git dir = %v, want %v", argv, got, c.writes)
			}
			if last := argv[len(argv)-1]; last != c.rec.Prompt && last != c.rec.Session {
				t.Fatalf("argv %q must end with the prompt or session", argv)
			}
		})
	}
}
