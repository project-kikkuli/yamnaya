package slot

import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Grace is how long a command's process group has to exit after SIGTERM before it is killed.
var Grace = 5 * time.Second

// Run runs argv at niceness nice (never raising its priority) in its own process group, passing signals to that group.
// When the command exits, whatever it left running in its group is ended too, so nothing outlives the slot.
func Run(argv []string, nice int, env []string) (int, error) {
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = env
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if nice > niceness() {
		// on macOS niceness is per process, so the child starts at it; on Linux renice the group once it exists
		unix.Setpriority(unix.PRIO_PROCESS, 0, nice)
	}
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(signals)
	if err := cmd.Start(); err != nil {
		return 127, err
	}
	pgid := cmd.Process.Pid
	if runtime.GOOS == "linux" && nice > 0 {
		unix.Setpriority(unix.PRIO_PGRP, pgid, nice)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	var signalled bool
wait:
	for {
		select {
		case err = <-done:
			break wait
		case sig := <-signals:
			signalled = true
			syscall.Kill(-pgid, sig.(syscall.Signal))
			go func() {
				time.Sleep(Grace)
				syscall.Kill(-pgid, syscall.SIGKILL)
			}()
		}
	}
	End(pgid)
	code := cmd.ProcessState.ExitCode()
	if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		code = 128 + int(ws.Signal())
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) || signalled {
		err = nil
	}
	return code, err
}

// End stops what is left in process group pgid: SIGTERM, then SIGKILL after Grace.
func End(pgid int) {
	if syscall.Kill(-pgid, syscall.SIGTERM) != nil {
		return
	}
	for deadline := time.Now().Add(Grace); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if syscall.Kill(-pgid, 0) != nil {
			return
		}
	}
	syscall.Kill(-pgid, syscall.SIGKILL)
}

func niceness() int {
	n, err := unix.Getpriority(unix.PRIO_PROCESS, 0)
	if err != nil {
		return 0
	}
	if runtime.GOOS == "linux" {
		return 20 - n
	}
	return n
}
