package cli

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/project-kikkuli/yamnaya/internal/tui"
)

// childrenEnv hands a restarted ym the detached children it must reap, since exec drops the goroutines waiting on them.
const childrenEnv = "YM_CHILDREN"

var children sync.Map

// reap waits for pid in the background, keeping it listed for a restart until it exits; the token keeps a reused pid listed.
func reap(pid int, wait func()) {
	token := new(int)
	children.Store(pid, token)
	go func() {
		wait()
		children.CompareAndDelete(pid, token)
	}()
}

// adoptChildren reaps the detached children a previous ym image left to this one.
func adoptChildren() {
	list := os.Getenv(childrenEnv)
	os.Unsetenv(childrenEnv)
	for _, f := range strings.Split(list, ",") {
		if pid, err := strconv.Atoi(f); err == nil {
			reap(pid, func() {
				var ws syscall.WaitStatus
				syscall.Wait4(pid, &ws, 0, nil)
			})
		}
	}
}

// Restart replaces this process with the ym now on disk, keeping its pid, args and environment.
func Restart() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var pids []string
	children.Range(func(k, _ any) bool {
		pids = append(pids, strconv.Itoa(k.(int)))
		return true
	})
	return syscall.Exec(self, os.Args, append(os.Environ(), childrenEnv+"="+strings.Join(pids, ","), tui.RestartedEnv+"=1"))
}

// serveLockEnv hands a restarting ym serve's lock to its new image, so no other ym starts a second serve while the new one loads.
const serveLockEnv = "YM_SERVE_LOCK_FD"

// keepServeLockAcrossExec leaves a duplicate of lock's descriptor open across exec and names it for the next image. The duplicate
// shares the lock, so the lock stays held when lock itself closes.
func keepServeLockAcrossExec(lock *os.File) error {
	fd, err := syscall.Dup(int(lock.Fd()))
	if err != nil {
		return err
	}
	return os.Setenv(serveLockEnv, strconv.Itoa(fd))
}

// inheritedServeLock is the serve lock at path a previous image of this ym serve still holds for it, or nil.
func inheritedServeLock(path string) *os.File {
	v := os.Getenv(serveLockEnv)
	os.Unsetenv(serveLockEnv)
	fd, err := strconv.Atoi(v)
	if err != nil {
		return nil
	}
	syscall.CloseOnExec(fd)
	f := os.NewFile(uintptr(fd), path)
	have, err := f.Stat()
	if want, wantErr := os.Stat(path); err != nil || wantErr != nil || !os.SameFile(have, want) || syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB) != nil {
		f.Close()
		return nil
	}
	return f
}
