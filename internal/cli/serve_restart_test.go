package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestARestartingServeKeepsItsLockForItsNextImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "work-service.lock")
	lock, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv(serveLockEnv) })
	if err := keepServeLockAcrossExec(lock); err != nil {
		t.Fatal(err)
	}
	lock.Close()
	other, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
		t.Fatal("another ym took the lock while serve was between images")
	}
	next := inheritedServeLock(path)
	if next == nil {
		t.Fatal("the next image did not get the lock it was handed")
	}
	if os.Getenv(serveLockEnv) != "" {
		t.Fatal("the lock's descriptor stayed named for this image's children")
	}
	next.Close()
	if err := syscall.Flock(int(other.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("the lock stayed held after serve let it go: %v", err)
	}
}

func TestAServeStartedWhileOneRunsLeavesTheLogAlone(t *testing.T) {
	for _, c := range []struct {
		name    string
		logged  bool
		wantErr string
	}{
		{"started in the background", true, ""},
		{"run in a terminal", false, "work service already running"},
	} {
		t.Run(c.name, func(t *testing.T) {
			state := t.TempDir()
			t.Setenv("YAMNAYA_STATE_DIR", state)
			t.Setenv("YAMNAYA_NO_SERVE", "1")
			lock, err := os.OpenFile(filepath.Join(state, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join(state, "work-service.log")
			log, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			root := NewRoot()
			root.SetArgs([]string{"serve", "--discover=false", "--once", "--briefs="})
			if c.logged {
				root.SetErr(log)
			} else {
				root.SetErr(&bytes.Buffer{})
			}
			err = root.Execute()
			if got := ""; err != nil {
				got = err.Error()
				if c.wantErr == "" || !strings.Contains(got, c.wantErr) {
					t.Fatalf("serve = %v, want %q", err, c.wantErr)
				}
			} else if c.wantErr != "" {
				t.Fatalf("serve = nil, want %q", c.wantErr)
			}
			if out, _ := os.ReadFile(logPath); len(out) != 0 {
				t.Fatalf("log = %q, want nothing", out)
			}
		})
	}
}
