package cli

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	return syscall.Exec(self, os.Args, append(os.Environ(), childrenEnv+"="+strings.Join(pids, ",")))
}
