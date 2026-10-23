package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
)

func TestServeForADeletedStateDirEndsTheAgentsItLaunched(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name    string
		deleted bool
	}{
		{"state dir deleted", true},
		{"state dir kept", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			state := filepath.Join(base, "state")
			socket := fmt.Sprintf("ymtest-%d-%d", os.Getpid(), time.Now().UnixNano())
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("YAMNAYA_YM_SOCKET", socket)
			srv := &tmux.Host{Socket: socket, Kind: tmux.Server}
			t.Cleanup(func() { exec.Command("tmux", "-L", socket, "kill-server").Run() })
			conf := filepath.Join(base, "tmux.conf")
			if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0o644); err != nil {
				t.Fatal(err)
			}
			pane, err := srv.Start(conf, "agent", base, []string{"sleep", "60"})
			if err != nil {
				t.Fatal(err)
			}

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			root := NewRoot()
			root.SetArgs([]string{"serve", "--discover=false", "--interval=50ms", "--briefs="})
			root.SetErr(&testWriter{t})
			done := make(chan error, 1)
			go func() { done <- root.ExecuteContext(ctx) }()
			for end := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
				if _, err := os.Stat(filepath.Join(state, "work-service.json")); err == nil {
					break
				}
				if time.Now().After(end) {
					t.Fatal("ym serve never completed a cycle")
				}
			}
			if c.deleted {
				if err := os.RemoveAll(state); err != nil {
					t.Fatal(err)
				}
			}

			select {
			case err := <-done:
				if !c.deleted {
					t.Fatalf("ym serve stopped on a kept state dir: %v", err)
				}
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				if c.deleted {
					t.Fatal("ym serve kept serving a deleted state dir")
				}
				cancel()
				<-done
			}
			_, paneErr := srv.Pane("agent")
			agentRuns := syscall.Kill(pane.PID, 0) == nil
			if (paneErr == nil) == c.deleted || agentRuns == c.deleted {
				t.Fatalf("pane err %v, agent runs %v; want agent ended %v", paneErr, agentRuns, c.deleted)
			}
		})
	}
}

type testWriter struct{ t *testing.T }

func (w *testWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
