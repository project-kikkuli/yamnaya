package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func server(t *testing.T) *Host {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	h := &Host{Socket: fmt.Sprintf("ymtest-%d-%d", os.Getpid(), time.Now().UnixNano())}
	if err := exec.Command("tmux", "-L", h.Socket, "-f", "/dev/null", "start-server", ";", "set-option", "-g", "exit-empty", "off").Run(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { exec.Command("tmux", "-L", h.Socket, "kill-server").Run() })
	return h
}

func (h *Host) must(t *testing.T, args ...string) string {
	t.Helper()
	out, err := h.run(args...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestLocateTypeAndScreenByPaneTTY(t *testing.T) {
	h := server(t)
	h.must(t, "new-session", "-d", "-s", "work", "-x", "120", "-y", "30", "cat")
	tty := h.must(t, "display-message", "-p", "-t", "work", "#{pane_tty}")
	hd, ok := h.Locate(tty)
	if !ok || hd.Label != "work:0.0" || !strings.HasPrefix(hd.ID, "%") {
		t.Fatalf("Locate(%s) = %+v, %v", tty, hd, ok)
	}
	if _, ok := h.Locate("/dev/nope"); ok {
		t.Fatal("located an unknown tty")
	}
	if err := h.Type(hd, "re D1: ship it"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cat to echo the typed line back", func() bool {
		s, err := h.Screen(hd)
		return err == nil && strings.Count(s, "re D1: ship it") == 2
	})
}

func TestFocusMovesTheClientWithoutChangingLayout(t *testing.T) {
	h := server(t)
	h.must(t, "new-session", "-d", "-s", "home", "-x", "120", "-y", "30", "cat")
	h.must(t, "new-session", "-d", "-s", "agents", "-x", "120", "-y", "30", "cat")
	h.must(t, "split-window", "-d", "-t", "agents:0", "cat")
	h.must(t, "new-window", "-d", "-t", "agents:", "cat")
	target := h.must(t, "display-message", "-p", "-t", "agents:0.1", "#{pane_id}")
	if _, err := h.Focus(core.Handle{ID: target, Label: "agents:0.1"}); err == nil {
		t.Fatal("focus with no attached client should fail")
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script not installed")
	}
	client := exec.Command("script", "-q", "/dev/null", "tmux", "-L", h.Socket, "attach", "-t", "home")
	client.Env = append(os.Environ(), "TERM=xterm-256color")
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Process.Kill(); client.Wait() })
	waitFor(t, "a client to attach", func() bool { return len(h.clients()) == 1 })
	layout := func() string {
		return h.must(t, "list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index} #{pane_id} #{window_panes} #{window_zoomed_flag}")
	}
	before := layout()
	outer, err := h.Focus(core.Handle{ID: target, Label: "agents:0.1"})
	if err != nil {
		t.Fatal(err)
	}
	c := h.clients()[0]
	if outer != c.tty {
		t.Errorf("outer = %q, want the client's tty %q", outer, c.tty)
	}
	shown := h.must(t, "display-message", "-p", "-c", c.tty, "#{session_name} #{pane_id}")
	if shown != "agents "+target {
		t.Errorf("client shows %q, want agents %s", shown, target)
	}
	if after := layout(); after != before {
		t.Errorf("layout changed:\n%s\n%s", before, after)
	}
}
