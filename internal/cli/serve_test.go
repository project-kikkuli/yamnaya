package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/harness/claude"
	"github.com/project-kikkuli/yamnaya/internal/health"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestServingAnAskingSessionSpawnsNoModelAndFilesNoItem(t *testing.T) {
	dir := t.TempDir()
	bin, marker := filepath.Join(dir, "bin"), filepath.Join(dir, "spawned")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "codex"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\necho "+name+" >> "+marker+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("HOME", dir)
	id := "55555555-eeee-4eee-8eee-555555555555"
	transcript := filepath.Join(dir, "claude", "projects", "-Users-me-app", id+".jsonl")
	for path, body := range map[string]string{
		filepath.Join(dir, "claude", "sessions", "7.json"): `{"pid":7,"sessionId":"` + id + `","cwd":"/Users/me/app","status":"idle"}`,
		transcript: `{"type":"user","timestamp":"2026-10-22T02:00:00Z","origin":{"kind":"human"},"message":{"content":"check the box"}}` + "\n" +
			`{"type":"assistant","timestamp":"2026-10-22T02:01:00Z","message":{"content":[{"type":"text","text":"Please review #42.\n\nOpen for you:\n4. Keep the login or switch?"}],"stop_reason":"end_turn"}}` + "\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	st, err := core.OpenStore(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	c := &core.Core{Store: st, Ask: health.Addressed, StateDir: dir, Harnesses: []core.Harness{&claude.Adapter{Dir: filepath.Join(dir, "claude"), Now: time.Now}},
		Procs: func() ([]proc.Proc, error) {
			return []proc.Proc{{PID: 7, PPID: 1, TTY: "/dev/ttys007", Args: []string{"claude"}}}, nil
		}}
	for range 3 {
		if _, err := serveCycle(c); err != nil {
			t.Fatal(err)
		}
	}
	b, err := c.Board()
	if err != nil {
		t.Fatal(err)
	}
	waiting := 0
	for _, n := range b.Needs {
		if n.Waiting() {
			waiting++
		}
	}
	items, err := st.Items(core.ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if waiting != 1 || len(items) != 0 {
		t.Fatalf("waiting rows %d, items %+v; want the session's one waiting row and no item", waiting, items)
	}
	if spawned, err := os.ReadFile(marker); !os.IsNotExist(err) {
		t.Fatalf("serving spawned a model: %q %v", spawned, err)
	}
	for _, name := range []string{"handoff-run", "handoff"} {
		if cmd, _, err := NewRoot().Find([]string{name}); err == nil && cmd.Name() == name {
			t.Fatalf("ym still has a %s command that can start a classifier", name)
		}
	}
}
