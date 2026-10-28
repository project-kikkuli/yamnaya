package tmux

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// fakeTmux puts a tmux on PATH that runs script with tmux's arguments in "$@".
func fakeTmux(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestATmuxThatFailsIsUnknownNeverEmpty(t *testing.T) {
	for _, tc := range []struct{ name, script, says string }{
		{"killed", "kill -9 $$", "signal: killed"},
		{"not found", "echo 'tmux: command not found' >&2; exit 127", "command not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeTmux(t, tc.script)
			h := &Host{Socket: Server, Kind: Server}
			if _, ok, err := h.Locate("/dev/ttys011"); ok || err == nil || !strings.Contains(err.Error(), tc.says) {
				t.Errorf("Locate = %v, %v; want the tmux error", ok, err)
			}
			if _, err := h.Panes(); err == nil {
				t.Error("Panes read an empty table")
			}
			if _, ok, err := h.Running("%24"); ok || err == nil {
				t.Errorf("Running = %v, %v; want the tmux error, not an exited agent", ok, err)
			}
			if c, err := h.Attach(core.Handle{ID: "%24"}); c != nil || err == nil {
				t.Errorf("Attach = %v, %v; want the tmux error", c, err)
			}
			if _, err := h.Pane("repo-wt-x"); err == nil || errors.Is(err, core.ErrNotFound) {
				t.Errorf("Pane = %v; want an error that is not gone", err)
			}
		})
	}
}

func TestFocusOutsideItsServerSelectsThePaneAndNamesTheLatestClient(t *testing.T) {
	for _, tc := range []struct {
		name, clients, outer string
		noHost               bool
	}{
		{"two clients", "100\t/dev/ttys002\n300\t/dev/ttys009\n200\t/dev/ttys005", "/dev/ttys009", false},
		{"no client", "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			fakeTmux(t, `echo "$@" >> `+calls+`; case "$*" in *list-clients*) printf '`+strings.ReplaceAll(tc.clients, "\n", `\n`)+`';; esac`)
			t.Setenv("TMUX", "")
			outer, err := (&Host{}).Focus(core.Handle{ID: "%7"})
			if outer != tc.outer || errors.Is(err, core.ErrNoHost) != tc.noHost {
				t.Fatalf("Focus = %q, %v; want %q", outer, err, tc.outer)
			}
			got, _ := os.ReadFile(calls)
			if !strings.Contains(string(got), "select-window -t %7 ; select-pane -t %7") {
				t.Fatalf("tmux calls %q, want the pane selected", got)
			}
		})
	}
	fakeTmux(t, "exit 1")
	if _, err := (&Host{Kind: Server}).Focus(core.Handle{ID: "%7"}); !errors.Is(err, core.ErrNoHost) {
		t.Fatalf("ym's server Focus outside it = %v, want ErrNoHost so it attaches", err)
	}
}
