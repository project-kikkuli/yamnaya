package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/signals"
)

func TestHarnessThatExitsAtStartIsReportedAndKept(t *testing.T) {
	cases := []struct {
		name         string
		args         func(dir string) []string
		project, win string
	}{
		{"spawn", func(string) []string { return []string{"spawn", "w1", "-p", "alpha"} }, "alpha", "w1"},
		{"new", func(dir string) []string { return []string{"new", "beta", "--cwd", dir} }, "beta", "manager"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newTestEnv(t)
			e.project("alpha")
			dying := filepath.Join(e.dir, "dying-harness")
			if err := os.WriteFile(dying, []byte("#!/bin/sh\necho 'error: no API key configured'\nexit 1\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("YAMNAYA_CLAUDE_BIN", dying)
			root := NewRoot()
			root.SetArgs(c.args(e.dir))
			err := root.Execute()
			if err == nil || !strings.Contains(err.Error(), "exited right after starting") || !strings.Contains(err.Error(), "no API key configured") {
				t.Fatalf("err = %v", err)
			}
			project, name := c.project, c.win
			if out := e.ym("ls", "-p", project); !strings.Contains(out, name) {
				t.Fatalf("row for %s was closed:\n%s", name, out)
			}
			snap, err := signals.NewReader(e.a.store, e.a.tmux).Snapshot()
			if err != nil {
				t.Fatal(err)
			}
			p, _ := snap.Project(project)
			w, _ := p.Window(name)
			if got := w.Label(time.Now()); got != "exited: error: no API key configured" {
				t.Fatalf("board label %q", got)
			}
		})
	}
}
