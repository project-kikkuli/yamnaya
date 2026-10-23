package cli

import (
	"fmt"
	"os"
	"slices"
	"testing"
)

func TestAStoppedServeIsStartedByLaunchdWhenItKeepsOne(t *testing.T) {
	for _, c := range []struct {
		name  string
		kept  bool
		calls []string
	}{
		{"launchd keeps a serve here", true, []string{"print", "kickstart"}},
		{"nothing keeps one", false, []string{"print"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			f, _ := fakeLaunchctl(t)
			state := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			label, err := serveLabel(state)
			if err != nil {
				t.Fatal(err)
			}
			if c.kept {
				f.loaded[fmt.Sprintf("gui/%d/%s", os.Getuid(), label)] = true
			}
			if err := startWorkService(); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(f.calls, c.calls) {
				t.Fatalf("launchctl %v, want %v", f.calls, c.calls)
			}
		})
	}
}

func TestServeDropsTheAgentSessionItWasStartedFrom(t *testing.T) {
	for k, v := range map[string]string{
		"TMUX": "/private/tmp/tmux-501/ym,2409,15", "TMUX_PANE": "%15", "CLAUDECODE": "1", "CLAUDE_CODE_SESSION_ID": "84e1c954",
		"CLAUDE_CODE_MESSAGING_SOCKET": "/tmp/cc-socks/1.sock", "CODEX_THREAD_ID": "t1", "CODEX_HOME": "/codex", "LANG": "en_US.UTF-8",
	} {
		t.Setenv(k, v)
	}
	scrubAgentEnv()
	for _, k := range []string{"TMUX", "TMUX_PANE", "CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_MESSAGING_SOCKET", "CODEX_THREAD_ID"} {
		if v, ok := os.LookupEnv(k); ok {
			t.Errorf("%s=%s survived", k, v)
		}
	}
	for k, want := range map[string]string{"CODEX_HOME": "/codex", "LANG": "en_US.UTF-8"} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s=%q, want %q kept", k, got, want)
		}
	}
}
