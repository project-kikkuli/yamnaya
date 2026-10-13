package cli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
)

var socketSeq atomic.Int64

type testEnv struct {
	t      *testing.T
	a      *app
	dir    string
	socket string
}

// newTestEnv points ym at a temp state dir, a private tmux socket, and a harness that just sleeps.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	fake := filepath.Join(dir, "fake-harness")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	socket := fmt.Sprintf("ymcli-%d-%d", os.Getpid(), socketSeq.Add(1))
	for k, v := range map[string]string{
		"YAMNAYA_STATE_DIR":   filepath.Join(dir, "state"),
		"YAMNAYA_TMUX_SOCKET": socket,
		"YAMNAYA_CLAUDE_BIN":  fake,
		"YAMNAYA_CODEX_BIN":   fake,
		"CODEX_HOME":          filepath.Join(dir, "codex"),
		"CLAUDE_CONFIG_DIR":   filepath.Join(dir, "claude"),
		"YM_PROJECT":          "",
		"YM_WINDOW":           "",
		"TMUX":                "",
		"TMUX_PANE":           "",
		"YAMNAYA_START_GRACE": "300ms",
	} {
		t.Setenv(k, v)
	}
	a, err := openApp("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		a.close()
		tmux.Client{Socket: socket + "-outer"}.KillServer()
		tmux.Client{Socket: socket}.KillServer()
	})
	return &testEnv{t: t, a: a, dir: dir, socket: socket}
}

func (e *testEnv) ym(args ...string) string {
	e.t.Helper()
	root := NewRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		e.t.Fatalf("ym %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
	return strings.TrimSpace(out.String())
}

func (e *testEnv) tmux(args ...string) string {
	e.t.Helper()
	out, err := exec.Command("tmux", append([]string{"-L", e.socket}, args...)...).CombinedOutput()
	if err != nil {
		e.t.Fatalf("tmux %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

func (e *testEnv) project(name string, workers ...string) {
	e.t.Helper()
	e.ym("new", name, "--cwd", e.dir, "--phases", "plan,build,ship")
	for _, w := range workers {
		e.ym("spawn", w, "-p", name)
	}
}

func (e *testEnv) item(project, source, kind string) store.Item {
	e.t.Helper()
	it, err := e.a.store.AddItem(store.Item{Project: project, Kind: kind, Question: "q " + source, SourceWindow: source})
	if err != nil {
		e.t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	return it
}

func (e *testEnv) target(project, window string) string {
	e.t.Helper()
	w, err := e.a.store.OpenWindow(project, window)
	if err != nil {
		e.t.Fatal(err)
	}
	return w.TmuxTarget
}

// attach puts a real client on the server, the way a terminal attached to tmux would be, and lets ym switch it.
func (e *testEnv) attach(session string) {
	e.t.Helper()
	out, err := exec.Command("tmux", "-L", e.socket+"-outer", "new-session", "-d", "-x", "120", "-y", "40",
		"tmux", "-L", e.socket, "attach", "-t", session).CombinedOutput()
	if err != nil {
		e.t.Fatalf("outer tmux: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for e.clientWindow() == "" {
		if time.Now().After(deadline) {
			e.t.Fatal("client never attached")
		}
		time.Sleep(20 * time.Millisecond)
	}
	e.t.Setenv("TMUX", "/tmp/ym-test,1,0")
}

func (e *testEnv) clientWindow() string {
	out, _ := exec.Command("tmux", "-L", e.socket, "list-clients", "-F", "#{window_id}").Output()
	return strings.TrimSpace(string(out))
}

// showDialog swaps target's process for a fake harness that prints a captured permission dialog, then waits until it shows.
func (e *testEnv) showDialog(target, name string) {
	e.t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "signals", "testdata", name+".txt"))
	if err != nil {
		e.t.Fatal(err)
	}
	e.tmux("respawn-pane", "-k", "-t", target, "sh", "-c", "cat '"+path+"'; exec sleep 600")
	for deadline := time.Now().Add(3 * time.Second); !strings.Contains(e.tmux("capture-pane", "-p", "-t", target), "cancel"); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			e.t.Fatalf("dialog %s never showed in %s", name, target)
		}
	}
}
