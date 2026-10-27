package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
)

func TestOnlyARightClickOnTheHostOpensALinkAtTheAttachingEnd(t *testing.T) {
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script not installed")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	dir, tmp := t.TempDir(), t.TempDir()
	t.Setenv("YAMNAYA_STATE_DIR", dir)
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ym := filepath.Join(tmp, "ym")
	if err := os.WriteFile(ym, []byte("#!/bin/sh\n"+testRun+"=1 exec '"+self+"' \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := filepath.Join(tmp, "tmux.conf")
	if err := os.WriteFile(conf, []byte(tmux.ServerConf+tmux.RelayOpen(ym, dir)), 0o644); err != nil {
		t.Fatal(err)
	}
	pass := func(seq string) string { return "\x1bPtmux;" + strings.ReplaceAll(seq, "\x1b", "\x1b\x1b") + "\x1b\\" }
	forged := pass(oscOpen+"https://evil.x/none\a") + pass(oscOpen+"AAAAAAAAAAAAAAAAAAAAAAAAAA;https://evil.x/guess\a") +
		pass(oscNonce+"mine\a") + pass(oscOpen+"mine;https://evil.x/mine\a") + oscOpen + "https://evil.x/raw\a"
	draw := filepath.Join(tmp, "draw")
	if err := os.WriteFile(draw, []byte(forged+"\x1b[H\x1b[2J\x1b]8;;https://e.x/a?b=1&c=2\x1b\\LINK\x1b]8;;\x1b\\"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &tmux.Host{Socket: tmuxtest.Socket(t), Kind: tmux.Server}
	if _, err := h.Start(conf, "s", tmp, []string{"sh", "-c", "read x; cat " + draw + "; read y"}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) string {
		c := exec.Command("tmux", append([]string{"-L", h.Socket}, args...)...)
		c.Env = tmux.AgentEnv(os.Environ())
		out, _ := c.Output()
		return string(out)
	}
	attach := linkRelayEnv + "=1 '" + ym + "' link-open none none none none && exec tmux -L " + h.Socket + " attach -t =s"
	args := []string{"-q", "/dev/null", "sh", "-c", attach}
	if runtime.GOOS == "linux" {
		args = []string{"-qfec", "sh -c " + shellQuote(attach), "/dev/null"}
	}
	var mu sync.Mutex
	var out bytes.Buffer
	var opens []string
	relay := &termRelay{out: &out, open: func(u []byte) { opens = append(opens, string(u)) }}
	locked := writerFunc(func(p []byte) (int, error) { mu.Lock(); defer mu.Unlock(); return relay.Write(p) })
	c := exec.Command(script, args...)
	c.Env = append(tmux.AgentEnv(os.Environ()), "TERM=xterm-256color")
	c.Stdout = locked
	in, err := c.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { run("kill-server"); in.Close(); c.Process.Kill(); c.Wait() })
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
	until("the client to attach", func() bool { return run("list-clients") != "" })
	tty := strings.TrimSpace(run("list-clients", "-F", "#{client_tty}"))
	if fi, err := os.Stat(linkNonce(dir, tty)); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("nonce for %s: %v %v", tty, fi, err)
	}
	if env := run("show-environment", "-g"); strings.Contains(env, linkRelayEnv) {
		t.Errorf("the server's environment, which panes inherit, has %s", linkRelayEnv)
	}
	run("send-keys", "-t", "=s:", "Enter")
	until("the forged opens and the link to be drawn", func() bool { return strings.Contains(out.String(), "LINK") })
	if len(opens) != 0 {
		t.Fatalf("pane output alone opened %q", opens)
	}
	in.Write([]byte("\x1b[<2;2;1M\x1b[<2;2;1m"))
	until("a right-click on the link to open it", func() bool { return len(opens) > 0 })
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if want := []string{"https://e.x/a?b=1&c=2"}; !slices.Equal(opens, want) {
		t.Errorf("opened %q, want %q", opens, want)
	}
}

type writerFunc func(p []byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
