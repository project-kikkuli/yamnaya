package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/profile"
)

func TestStatusCheckClosureCancelsItsNativeProcessAndRejectsNewAsks(t *testing.T) {
	root, bins := t.TempDir(), t.TempDir()
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	binary := "#!/bin/sh\necho started >> \"$CLAUDE_CONFIG_DIR/checks\"\nsleep 100000 &\nwait\n"
	if err := os.WriteFile(filepath.Join(bins, "claude"), []byte(binary), 0700); err != nil {
		t.Fatal(err)
	}
	checks := &statusChecks{timeout: 10 * time.Second}
	t.Cleanup(checks.Close)
	p := profile.Profile{Harness: "claude", ID: "test", Root: root, Explicit: true}
	done := checks.ask(p)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if data, err := os.ReadFile(filepath.Join(root, "checks")); err == nil && strings.Contains(string(data), "started") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native status fixture did not start")
		}
		time.Sleep(time.Millisecond)
	}
	closed := make(chan struct{})
	go func() { checks.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("closing did not cancel and join the native status check")
	}
	select {
	case <-done:
	default:
		t.Fatal("Close returned before native status check finished")
	}
	for range 3 {
		select {
		case <-checks.ask(p):
		default:
			t.Fatal("closed status checks accepted new work")
		}
	}
	data, err := os.ReadFile(filepath.Join(root, "checks"))
	if err != nil || string(data) != "started\n" {
		t.Fatalf("closed checks launched another native process: %q, %v", data, err)
	}
	checks.mu.Lock()
	defer checks.mu.Unlock()
	if len(checks.running) != 0 {
		t.Fatalf("Close left running checks: %+v", checks.running)
	}
}
