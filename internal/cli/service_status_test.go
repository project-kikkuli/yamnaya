package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestServiceStatusNamesTheServeHoldingTheLockNotTheLastHeartbeat(t *testing.T) {
	for _, c := range []struct {
		name    string
		held    bool
		holder  string
		wantPID int
		running bool
	}{
		{"a serve that has not cycled yet", true, "4242", 4242, true},
		{"no serve running", false, "", 1177, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			if err := os.WriteFile(filepath.Join(state, "work-service.json"), []byte(`{"pid":1177,"at":"2026-11-16T23:40:24Z","error":""}`), 0o600); err != nil {
				t.Fatal(err)
			}
			if c.held {
				lock, err := os.OpenFile(filepath.Join(state, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0o600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { lock.Close() })
				if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
					t.Fatal(err)
				}
				if _, err := lock.WriteString(c.holder); err != nil {
					t.Fatal(err)
				}
			}
			cmd := NewRoot()
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"service", "status"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			var st struct {
				PID     int  `json:"pid"`
				Running bool `json:"running"`
			}
			if err := json.Unmarshal(out.Bytes(), &st); err != nil {
				t.Fatalf("%v: %s", err, out.String())
			}
			if st.PID != c.wantPID || st.Running != c.running {
				t.Fatalf("status pid %d running %v, want pid %d running %v", st.PID, st.Running, c.wantPID, c.running)
			}
		})
	}
}
