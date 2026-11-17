package cli

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
)

func TestStoppingTheControlDaemonLeavesItsExistingAgentRunning(t *testing.T) {
	socket := tmuxtest.Socket(t)
	base := t.TempDir()
	state := filepath.Join(base, "state")
	conf := filepath.Join(base, "tmux.conf")
	if err := os.WriteFile(conf, []byte(tmux.ServerConf), 0600); err != nil {
		t.Fatal(err)
	}
	srv := &tmux.Host{Socket: socket, Kind: tmux.Server}
	pane, err := srv.Start(conf, "independent-agent", base, []string{"sleep", "60"})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "serve", "--discover=false", "--interval=50ms", "--briefs=")
	cmd.Env = append(os.Environ(), testRun+"=1", "YAMNAYA_STATE_DIR="+state, "YAMNAYA_YM_SOCKET="+socket, "YAMNAYA_NO_SERVE=1")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() { cmd.Process.Kill() })
	deadline := time.Now().Add(10 * time.Second)
	for {
		select {
		case err := <-done:
			t.Fatalf("daemon exited before heartbeat: %v", err)
		default:
		}
		raw, _ := os.ReadFile(filepath.Join(state, "work-service.json"))
		var heartbeat struct {
			PID int `json:"pid"`
		}
		if json.Unmarshal(raw, &heartbeat) == nil && heartbeat.PID == cmd.Process.Pid {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("daemon did not reach reconciliation")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("daemon shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon did not stop")
	}
	current, err := srv.Pane("independent-agent")
	if err != nil || current.Dead || current.PID != pane.PID || syscall.Kill(pane.PID, 0) != nil {
		t.Fatalf("daemon shutdown ended the agent: original %+v, current %+v, %v", pane, current, err)
	}
}
