package cli

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/harness"
)

func TestCarryEnvSetsYmVariablesForCodexShellsOnly(t *testing.T) {
	env := map[string]string{"YM_PROJECT": "alpha", "YM_WINDOW": "w 1", "YAMNAYA_STATE_DIR": "/s", "CODEX_HOME": "/c"}
	got := strings.Join(harness.CarryEnv(harness.Codex, []string{"codex", "resume", "t-1"}, env), " ")
	want := `codex -c shell_environment_policy.set.YAMNAYA_STATE_DIR="/s" -c shell_environment_policy.set.YM_PROJECT="alpha" -c shell_environment_policy.set.YM_WINDOW="w 1" resume t-1`
	if got != want {
		t.Fatalf("codex argv\n got %s\nwant %s", got, want)
	}
	if got := harness.CarryEnv(harness.Claude, []string{"claude", "x"}, env); strings.Join(got, " ") != "claude x" {
		t.Fatalf("claude argv changed: %v", got)
	}
}

func TestCodexWindowsCarryYmEnvironment(t *testing.T) {
	e := newTestEnv(t)
	e.ym("new", "alpha", "--cwd", e.dir, "--harness", "codex")
	e.ym("spawn", "w1", "-p", "alpha", "--harness", "codex")
	for _, name := range []string{"manager", "w1"} {
		cmd := e.tmux("display-message", "-p", "-t", e.target("alpha", name), "#{pane_start_command}")
		for _, want := range []string{`shell_environment_policy.set.YM_PROJECT="alpha"`, `shell_environment_policy.set.YM_WINDOW="` + name + `"`, `shell_environment_policy.set.YAMNAYA_STATE_DIR=`} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s started as %s; missing %s", name, cmd, want)
			}
		}
	}
}

func TestSwitchJudgesCodexBusyByItsTurnNotItsScreen(t *testing.T) {
	cases := []struct {
		name   string
		events []string
		busy   bool
	}{
		{"turn open", []string{"task_started"}, true},
		{"turn finished", []string{"task_started", "task_complete"}, false},
		{"turn aborted", []string{"task_started", "turn_aborted"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			redraw := filepath.Join(e.dir, "redrawing-codex")
			os.WriteFile(redraw, []byte("#!/bin/sh\nwhile :; do echo frame; sleep 0.1; done\n"), 0o755)
			cs := filepath.Join(e.dir, "cs")
			os.WriteFile(cs, []byte("#!/bin/sh\necho ported-$2\n"), 0o755)
			t.Setenv("YAMNAYA_CODEX_BIN", redraw)
			t.Setenv("YAMNAYA_CS_BIN", cs)
			e.ym("new", "alpha", "--cwd", e.dir, "--harness", "codex")
			mgr, _ := e.a.store.OpenWindow("alpha", "manager")
			if err := e.a.store.SetSessionID(mgr.ID, "t-1"); err != nil {
				t.Fatal(err)
			}
			var rollout strings.Builder
			for _, ev := range c.events {
				rollout.WriteString(`{"type":"event_msg","payload":{"type":"` + ev + `"}}` + "\n")
			}
			path := filepath.Join(e.dir, "codex", "sessions", "2026", "10", "13", "rollout-2026-10-13T09-00-00-t-1.jsonl")
			os.MkdirAll(filepath.Dir(path), 0o755)
			os.WriteFile(path, []byte(rollout.String()), 0o644)
			root := NewRoot()
			root.SetOut(io.Discard)
			root.SetArgs([]string{"-p", "alpha", "switch", "manager", "--to", "claude", "--min-idle", "5s"})
			err := root.Execute()
			if c.busy != (err != nil && strings.Contains(err.Error(), "mid-turn")) {
				t.Fatalf("busy=%v, err=%v", c.busy, err)
			}
			if !c.busy && err != nil {
				t.Fatalf("switch of an idle codex window failed: %v", err)
			}
		})
	}
}
