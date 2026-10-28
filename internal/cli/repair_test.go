package cli

import (
	"net"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
	"time"
)

func TestParseRemote(t *testing.T) {
	at := "2026-10-28T21:38:59.198989601Z"
	when, _ := time.Parse(time.RFC3339Nano, at)
	for _, c := range []struct {
		name, out          string
		installed, running bool
		version            string
		serveAt            time.Time
	}{
		{"current", "installed\nabc123\n\n{\"at\":\"" + at + "\",\"running\":true}\n", true, true, "abc123", when},
		{"too old to name its version", "installed\n\n{\"at\":\"" + at + "\",\"running\":false}\n", true, false, "", when},
		{"serve never started", "installed\nabc123\n\nBackground service has not started. Run ym or ym serve.\n", true, false, "abc123", time.Time{}},
		{"missing", "missing\n", false, false, "", time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := parseRemote(c.out)
			if r.Installed != c.installed || r.ServeRunning != c.running || r.Version != c.version || !r.ServeAt.Equal(c.serveAt) {
				t.Fatalf("parseRemote = %+v", r)
			}
		})
	}
}

func TestDeadSocketsAreOnlyThoseNothingListensOn(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ymrep")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	live, err := net.Listen("unix", filepath.Join(dir, "live"))
	if err != nil {
		t.Fatal(err)
	}
	defer live.Close()
	dead, err := net.Listen("unix", filepath.Join(dir, "dead"))
	if err != nil {
		t.Fatal(err)
	}
	dead.(*net.UnixListener).SetUnlinkOnClose(false)
	dead.Close()
	os.WriteFile(filepath.Join(dir, "plain"), nil, 0o600)
	if got := deadSockets(dir, filepath.Join(dir, "none-*")); !slices.Equal(got, []string{filepath.Join(dir, "dead")}) {
		t.Fatalf("dead sockets %v", got)
	}
}

func TestStaleLocksSkipLivePanesAndHeldLocks(t *testing.T) {
	dir := t.TempDir()
	for _, id := range []string{"1", "2", "3"} {
		os.WriteFile(filepath.Join(dir, "type-"+id+".lock"), nil, 0o600)
	}
	held, err := os.OpenFile(filepath.Join(dir, "type-3.lock"), os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	if got := staleLocks(dir, map[string]bool{"1": true}); !slices.Equal(got, []string{filepath.Join(dir, "type-2.lock")}) {
		t.Fatalf("stale locks %v", got)
	}
}
