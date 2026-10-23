// Package tmuxtest names private tmux servers for tests and ends each one even when its test binary is killed or times out.
package tmuxtest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

// Socket is a fresh tmux socket name for t. Its server and socket file are gone when t finishes, and by a watcher outside the test binary when the
// binary dies first, so a killed or timed-out run never leaves the server behind.
func Socket(t testing.TB) string {
	t.Helper()
	sock := fmt.Sprintf("ymtest-%d-%d", os.Getpid(), time.Now().UnixNano())
	tmux, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not installed")
	}
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	path := filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), sock)
	watch := exec.Command("/bin/sh", "-c", `while kill -0 "$1" 2>/dev/null; do sleep 1; done; "$2" -L "$3" kill-server 2>/dev/null; rm -f "$4"`,
		"ymtest-watch", strconv.Itoa(os.Getpid()), tmux, sock, path)
	watch.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := watch.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		exec.Command(tmux, "-L", sock, "kill-server").Run()
		os.Remove(path)
		watch.Process.Kill()
		watch.Wait()
	})
	return sock
}
