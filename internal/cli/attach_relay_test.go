package cli

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
)

func TestOnlyARightClickHereOpensALinkFromARemoteSession(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tmp := t.TempDir()
	conf := filepath.Join(tmp, "tmux.conf")
	if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0o644); err != nil {
		t.Fatal(err)
	}
	forge := func(body string) string {
		return "\x1bPtmux;\x1b\x1b]7477;open;" + body + "\a\x1b\\" + "\x1b]7477;open;" + body + "\a"
	}
	link := "https://e.x/a?b=1&c=2"
	steps := []string{
		forge("42;1;https://evil.x/unclicked") + "\x1b[H\x1b[2J\x1b]8;;" + link + "\x1b\\LINK\x1b]8;;\x1b\\",
		forge("60;12;https://evil.x/raced") + forge("60;12;https://evil.x/second") + forge("42;1;https://evil.x/othercell"),
	}
	script := "read x"
	for i, s := range steps {
		f := filepath.Join(tmp, "step"+string(rune('0'+i)))
		if err := os.WriteFile(f, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
		script += "; cat " + f + "; read x"
	}
	h := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
	p, err := h.Start(conf, "s", tmp, []string{"sh", "-c", script})
	if err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		c := exec.Command("tmux", append([]string{"-L", h.Socket}, args...)...)
		c.Env = tmux.AgentEnv(os.Environ())
		out, _ := c.Output()
		return string(out)
	}
	run("split-window", "-hb", "-t", p.ID, "sleep 600")
	run("select-pane", "-t", p.ID)
	owner, in, err := openPTY()
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.IoctlSetWinsize(int(owner.Fd()), unix.TIOCSWINSZ, &unix.Winsize{Row: 24, Col: 80}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var out bytes.Buffer
	var opens []string
	clicks := &rightClicks{}
	relay := &termRelay{out: writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return out.Write(p) }),
		open: func(u []byte) { mu.Lock(); opens = append(opens, string(u)); mu.Unlock() }, clicked: clicks.take}
	attach := exec.Command("tmux", "-L", h.Socket, "attach", "-t", "=s")
	attach.Env = append(tmux.AgentEnv(os.Environ()), "TERM=xterm-256color")
	done := make(chan error, 1)
	go func() { done <- proxyTTY(attach, in, clicks.see, relay) }()
	t.Cleanup(func() {
		run("kill-server")
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("the relay outlived its session")
		}
		owner.Close()
		in.Close()
	})
	until := func(what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			mu.Lock()
			done := ok()
			mu.Unlock()
			if done {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	opened := func(want ...string) {
		t.Helper()
		time.Sleep(300 * time.Millisecond)
		mu.Lock()
		defer mu.Unlock()
		if !slices.Equal(opens, want) {
			t.Fatalf("opened %q, want %q", opens, want)
		}
	}
	until("the client to attach", func() bool { return run("list-clients") != "" })
	if size := strings.TrimSpace(run("list-clients", "-F", "#{client_width}x#{client_height}")); size != "80x24" {
		t.Errorf("the session's client is %s, want this terminal's 80x24", size)
	}
	owner.Write([]byte("\r"))
	until("what the pane printed to be drawn", func() bool { return strings.Contains(out.String(), "LINK") })
	opened()
	if left := strings.TrimSpace(run("display-message", "-p", "-t", p.ID, "#{pane_left}")); left != "41" {
		t.Fatalf("the link's pane starts at column %s, want 41", left)
	}
	owner.Write([]byte("\x1b[<2;42;1M\x1b[<2;42;1m"))
	until("the right-clicked link to open", func() bool { return len(opens) > 0 })
	opened(link)
	owner.Write([]byte("\x1b[<2;60;12M\x1b[<2;60;12m\r"))
	opened(link, "https://evil.x/raced")
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestAttachRelayGivesTheTerminalBackWhenToldToEnd(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(ssh, []byte("#!/bin/sh\nexec sleep 600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, sig := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP} {
		t.Run(sig.String(), func(t *testing.T) {
			owner, tty, err := openPTY()
			if err != nil {
				t.Fatal(err)
			}
			defer owner.Close()
			defer tty.Close()
			go io.Copy(io.Discard, owner)
			cooked := func() bool {
				tio, err := unix.IoctlGetTermios(int(tty.Fd()), getTermios)
				return err == nil && tio.Lflag&unix.ICANON != 0 && tio.Lflag&unix.ECHO != 0
			}
			if !cooked() {
				t.Fatal("the terminal starts raw")
			}
			c := exec.Command(self, "attach-relay", "vm.example")
			c.Env = append(os.Environ(), testRun+"=1", "YAMNAYA_SSH_BIN="+ssh)
			c.Stdin, c.Stdout, c.Stderr = tty, tty, tty
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			waited := make(chan error, 1)
			go func() { waited <- c.Wait() }()
			for deadline := time.Now().Add(5 * time.Second); cooked(); time.Sleep(20 * time.Millisecond) {
				if time.Now().After(deadline) {
					c.Process.Kill()
					t.Fatal("ym attach-relay never put the terminal in raw mode")
				}
			}
			c.Process.Signal(sig)
			select {
			case <-waited:
			case <-time.After(5 * time.Second):
				c.Process.Kill()
				t.Fatalf("ym attach-relay outlived %v", sig)
			}
			if !cooked() {
				t.Errorf("after %v the terminal is left raw", sig)
			}
		})
	}
}
