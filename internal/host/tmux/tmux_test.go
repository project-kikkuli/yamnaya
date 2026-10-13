package tmux

import (
	"errors"
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

func TestFocusJumpsOnlyYmsOwnClientWithoutChangingLayout(t *testing.T) {
	h := server(t)
	h.must(t, "new-session", "-d", "-s", "home", "cat")
	h.must(t, "new-session", "-d", "-s", "agents", "cat")
	h.must(t, "split-window", "-d", "-t", "agents:", "cat")
	h.must(t, "new-window", "-d", "-t", "agents:", "cat")
	target := h.must(t, "list-panes", "-t", "agents:^", "-F", "#{pane_id}")
	target = strings.Split(target, "\n")[1]
	if _, err := h.Focus(core.Handle{ID: target}); !errors.Is(err, core.ErrNoHost) {
		t.Fatalf("focus outside tmux = %v, want ErrNoHost", err)
	}
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("script not installed")
	}
	client := exec.Command("script", "-q", "/dev/null", "tmux", "-L", h.Socket, "attach", "-t", "home")
	client.Env = append(os.Environ(), "TERM=xterm-256color")
	stdin, err := client.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Process.Kill(); stdin.Close(); client.Wait() })
	waitFor(t, "a client to attach", func() bool {
		out, _ := h.run("list-clients", "-F", "#{client_tty}")
		return out != ""
	})
	layout := func() string {
		return h.must(t, "list-panes", "-a", "-F", "#{session_name}:#{window_index}.#{pane_index} #{pane_id} #{window_panes} #{window_zoomed_flag}")
	}
	before := layout()
	h.Self = h.must(t, "display-message", "-p", "-t", "home:", "#{pane_id}")
	sock := h.must(t, "display-message", "-p", "#{socket_path}")
	t.Setenv("TMUX", sock+",1,0")
	if _, err := h.Focus(core.Handle{ID: target}); err != nil {
		t.Fatal(err)
	}
	if shown := h.must(t, "list-clients", "-F", "#{session_name} #{pane_id}"); shown != "agents "+target {
		t.Errorf("client shows %q, want agents %s", shown, target)
	}
	if after := layout(); after != before {
		t.Errorf("layout changed:\n%s\n%s", before, after)
	}
}
