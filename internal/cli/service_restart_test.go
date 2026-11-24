package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestRestartCountsAServeThatTakesTheLockAtOnceAsStopped(t *testing.T) {
	for _, c := range []struct {
		name          string
		handover      func(lock *os.File)
		wantSuccessor bool
		wantErr       bool
	}{
		{"a kept serve takes the lock as the old one exits", func(lock *os.File) {
			lock.Truncate(0)
			lock.WriteAt([]byte(strconv.Itoa(os.Getpid())), 0)
		}, true, false},
		{"the lock comes free", func(lock *os.File) { lock.Close() }, false, false},
		{"the old serve keeps running", func(*os.File) {}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			lock, err := os.OpenFile(filepath.Join(dir, "work-service.lock"), os.O_CREATE|os.O_RDWR, 0600)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			const old = 1
			lock.WriteAt([]byte(strconv.Itoa(old)), 0)
			go func() { time.Sleep(100 * time.Millisecond); c.handover(lock) }()
			successor, err := awaitServeStop(context.Background(), dir, serveLockHolder(dir), 500*time.Millisecond)
			if (successor == os.Getpid()) != c.wantSuccessor || (err != nil) != c.wantErr {
				t.Fatalf("successor %d, err %v; want a successor %v, an error %v", successor, err, c.wantSuccessor, c.wantErr)
			}
		})
	}
}
