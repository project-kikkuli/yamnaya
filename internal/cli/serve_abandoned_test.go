package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
)

func TestServeForADeletedStateDirEndsTheAgentsItLaunched(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name    string
		deleted bool
		// byFile deletes the way rm -rf does, one file at a time while serve still writes, so the directory itself may stay.
		byFile bool
	}{
		{"state dir deleted at once", true, false},
		{"state dir deleted file by file", true, true},
		{"state dir kept", false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			base := t.TempDir()
			state := filepath.Join(base, "state")
			socket := tmuxtest.Socket(t)
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("YAMNAYA_YM_SOCKET", socket)
			srv := &tmux.Host{Socket: socket, Kind: tmux.Server}
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
				select {
				case err := <-done:
					t.Fatalf("ym serve exited before its first cycle: %v", err)
				default:
				}
				if _, err := os.Stat(filepath.Join(state, "work-service.json")); err == nil {
					break
				}
				if time.Now().After(end) {
					stack := make([]byte, 1<<20)
					n := runtime.Stack(stack, true)
					t.Fatalf("ym serve never completed a cycle; goroutines:\n%s", stack[:n])
				}
			}
			gone := filepath.Join(base, "gone")
			switch {
			case c.byFile:
				ents, err := os.ReadDir(state)
				if err != nil {
					t.Fatal(err)
				}
				for _, e := range ents {
					if err := os.RemoveAll(filepath.Join(state, e.Name())); err != nil {
						t.Fatal(err)
					}
					time.Sleep(40 * time.Millisecond)
				}
				gone = state
			case c.deleted:
				if err := os.Rename(state, gone); err != nil {
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
			if c.deleted {
				if err := os.RemoveAll(gone); err != nil {
					t.Fatal(err)
				}
			}
			_, paneErr := srv.Pane("agent")
			agentRuns := syscall.Kill(pane.PID, 0) == nil
			for end := time.Now().Add(5 * time.Second); c.deleted && agentRuns && time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
				agentRuns = syscall.Kill(pane.PID, 0) == nil
			}
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

func TestOnlyTheHomesOwnServeWritesTheMachinesOverloadFile(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	for _, c := range []struct {
		name   string
		scoped bool
	}{
		{"the home's own state dir", false},
		{"a state dir of its own", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := t.TempDir()
			state := filepath.Join(home, ".local", "state", "yamnaya")
			if c.scoped {
				state = filepath.Join(home, "drill", "state")
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_STATE_HOME", "")
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("YAMNAYA_YM_SOCKET", tmuxtest.Socket(t))
			overload := filepath.Join(home, ".local", "state", "agent-slot", "overload.json")
			ctx, cancel := context.WithCancel(context.Background())
			root := NewRoot()
			root.SetArgs([]string{"serve", "--interval=50ms", "--briefs="})
			root.SetErr(&testWriter{t})
			done := make(chan error, 1)
			go func() { done <- root.ExecuteContext(ctx) }()
			defer func() {
				cancel()
				<-done
			}()
			// the sampler writes at once and every 5s, so a scoped serve has had its chance well before the limit
			limit := 30 * time.Second
			if c.scoped {
				limit = 8 * time.Second
			}
			written := false
			for end := time.Now().Add(limit); !written && time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
				_, err := os.Stat(overload)
				written = err == nil
			}
			if written == c.scoped {
				t.Fatalf("scoped %v: machine's overload.json written %v", c.scoped, written)
			}
		})
	}
}

func TestAScopedServeOptedIntoTheDiskWatcherStartsItAndLeavesPressureAlone(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	home := t.TempDir()
	state := filepath.Join(home, "drill", "state")
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("YAMNAYA_STATE_DIR", state)
	t.Setenv("YAMNAYA_YM_SOCKET", tmuxtest.Socket(t))
	t.Setenv(core.DiskWatchEnv, "1")
	if core.RehearsalRoot(state) == "" {
		t.Fatalf("%s is not under a rehearsal root", state)
	}
	ctx, cancel := context.WithCancel(context.Background())
	root := NewRoot()
	root.SetArgs([]string{"serve", "--interval=50ms", "--briefs="})
	root.SetErr(&testWriter{t})
	done := make(chan error, 1)
	go func() { done <- root.ExecuteContext(ctx) }()
	defer func() {
		cancel()
		<-done
	}()
	lock := filepath.Join(state, "disk.lock")
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(lock); err == nil {
			break
		}
		if time.Now().After(end) {
			t.Fatal("the disk watcher never ticked")
		}
	}
	// the pressure sampler writes at once and every 5s
	time.Sleep(6 * time.Second)
	if _, err := os.Stat(filepath.Join(home, ".local", "state", "agent-slot", "overload.json")); err == nil {
		t.Fatal("a scoped serve wrote the machine's overload.json")
	}
}
