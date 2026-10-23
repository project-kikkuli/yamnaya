package tmux

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKillUsesTheMutationResultAndOnlyAcceptsConfirmedAbsence(t *testing.T) {
	for _, tc := range []struct {
		name, failure string
		wantError     bool
	}{
		{"unreadable pane status still permits the actual kill", "", false},
		{"already absent session", "can't find session: work", false},
		{"already absent server", "no server running on /tmp/fixture", false},
		{"already absent socket", "error connecting to /tmp/fixture (No such file or directory)", false},
		{"transport permission failure", "error connecting to /tmp/fixture (Permission denied)", true},
		{"server failed during mutation", "server exited unexpectedly", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			alive := filepath.Join(dir, "alive")
			os.WriteFile(alive, []byte("alive"), 0600)
			t.Setenv("YM_KILL_FIXTURE_ALIVE", alive)
			t.Setenv("YM_KILL_FIXTURE_FAILURE", tc.failure)
			script := "#!/bin/sh\nfor a; do case \"$a\" in list-panes|kill-session) cmd=$a; break;; esac; done\ncase \"$cmd\" in\nlist-panes) echo 'Permission denied' >&2; exit 1;;\nkill-session) if [ -n \"$YM_KILL_FIXTURE_FAILURE\" ]; then echo \"$YM_KILL_FIXTURE_FAILURE\" >&2; exit 1; fi; rm \"$YM_KILL_FIXTURE_ALIVE\";;\nesac\n"
			if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			h := Host{Socket: "fixture"}
			if err := h.Kill("work"); (err != nil) != tc.wantError {
				t.Fatalf("Kill error=%v, expected error=%t", err, tc.wantError)
			}
			if tc.failure == "" {
				if _, err := os.Stat(alive); !os.IsNotExist(err) {
					t.Fatal("kill reported success while the session was still alive")
				}
			}
		})
	}
}
