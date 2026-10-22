package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := h.Paste(hd, "re D1: ship it"); err != nil {
		t.Fatal(err)
	}
	if err := h.Enter(hd); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "cat to echo the typed line back", func() bool {
		s, err := h.Screen(hd)
		return err == nil && strings.Count(s, "re D1: ship it") == 2
	})
}

func TestLocateSkipsADeadPaneOnTheLiveSessionsReusedTTY(t *testing.T) {
	dead := "/dev/ttys006\t%9\tbuddy:0.0\tbuddy\t%9\t/dev/ttys006\t1\t0\t0\t"
	live := "/dev/ttys006\t%11\tcost:0.0\tcost\t%11\t/dev/ttys006\t0\t0\t412\t"
	for name, rows := range map[string][]string{"dead first": {dead, live}, "live first": {live, dead}} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			script := "#!/bin/sh\nprintf '%s\\n' '" + strings.Join(rows, "' '") + "'\n"
			if err := os.WriteFile(dir+"/tmux", []byte(script), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			hd, ok := (&Host{Socket: "fake"}).Locate("/dev/ttys006")
			if !ok || hd.ID != "%11" || hd.Label != "cost:0.0" {
				t.Fatalf("Locate = %+v, %v, want the live pane %%11", hd, ok)
			}
		})
	}
}

func TestPanesAreReadAgainOnlyAfterTerminalProcessesChange(t *testing.T) {
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := "#!/bin/sh\necho >> " + calls + "\nprintf '%s\\n' '/dev/ttys006\t%11\tcost:0.0\tcost\t%11\t/dev/ttys006\t0\t0\t412\t'\n"
	if err := os.WriteFile(filepath.Join(dir, "tmux"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	read := func() int {
		b, _ := os.ReadFile(calls)
		return strings.Count(string(b), "\n")
	}
	h := &Host{Socket: "fake"}
	h.Terminals(1)
	if _, ok := h.Locate("/dev/ttys006"); !ok || read() != 1 {
		t.Fatalf("first board: located %v after %d tmux calls", ok, read())
	}
	time.Sleep(600 * time.Millisecond)
	h.Terminals(1)
	if h.Locate("/dev/ttys006"); read() != 2 {
		t.Fatalf("%d tmux calls, want a second while tmux catches up with the change", read())
	}
	time.Sleep(settle)
	h.Terminals(1)
	if h.Panes(); read() != 2 {
		t.Fatalf("%d tmux calls, want none once the terminals stayed the same", read())
	}
	h.Terminals(2)
	if _, ok := h.Panes()["cost"]; !ok || read() != 3 {
		t.Fatalf("a terminal process change read tmux %d times, want 3", read())
	}
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

func TestSplitOpensTheTargetBesideYmWithoutMovingAnySessionAndClosesOnDetach(t *testing.T) {
	for _, tc := range []struct {
		name string
		// own puts the target in ym's server, and in ym's own session when session is "home".
		own     bool
		session string
	}{
		{"another server", false, "agents"},
		{"another session", true, "agents"},
		{"ym's own session", true, "home"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ym := server(t)
			ym.must(t, "set-option", "-g", "remain-on-exit", "on")
			ym.must(t, "new-session", "-d", "-s", "home", "-x", "200", "-y", "50", "cat")
			h := ym
			if !tc.own {
				h = server(t)
			}
			if tc.session != "home" {
				h.must(t, "new-session", "-d", "-s", tc.session, "cat")
			}
			h.must(t, "new-window", "-d", "-t", tc.session+":", "cat")
			target := h.must(t, "list-panes", "-t", tc.session+":1", "-F", "#{pane_id}")
			self := ym.must(t, "display-message", "-p", "-t", "home:0", "#{pane_id}")
			sessions := h.must(t, "list-sessions", "-F", "#{session_name}")

			h.Self = self
			t.Setenv("TMUX", "")
			if err := h.Split(core.Handle{ID: target}); !errors.Is(err, core.ErrNoHost) {
				t.Fatalf("split outside tmux = %v, want ErrNoHost", err)
			}
			t.Setenv("TMUX", ym.must(t, "display-message", "-p", "#{socket_path}")+",1,0")
			if err := h.Split(core.Handle{ID: target}); err != nil {
				t.Fatal(err)
			}
			if n := ym.must(t, "display-message", "-p", "-t", "=home:0", "#{window_panes}"); n != "2" {
				t.Errorf("ym's window has %s panes, want 2", n)
			}
			var client string
			waitFor(t, "a client showing the target", func() bool {
				out, _ := h.run("list-clients", "-F", "#{pane_id} #{client_name}")
				pane, name, _ := strings.Cut(out, " ")
				client = name
				return pane == target
			})
			if w := h.must(t, "display-message", "-p", "-t", "="+tc.session+":", "#{window_index}"); w != "0" {
				t.Errorf("session %s moved to window %s", tc.session, w)
			}
			if w := ym.must(t, "display-message", "-p", "-t", "=home:", "#{window_index}"); w != "0" {
				t.Errorf("ym's session moved to window %s", w)
			}

			h.must(t, "detach-client", "-t", client)
			waitFor(t, "the split to close", func() bool {
				n, _ := ym.run("display-message", "-p", "-t", "=home:0", "#{window_panes}")
				return n == "1"
			})
			if out := h.must(t, "list-sessions", "-F", "#{session_name}"); out != sessions {
				t.Errorf("sessions after detach = %q, want %q", out, sessions)
			}
		})
	}
}
