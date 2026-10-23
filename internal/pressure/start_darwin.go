package pressure

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func started(pid int) (int64, error) {
	p, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if errors.Is(err, syscall.ESRCH) || (err == nil && p.Proc.P_pid != int32(pid)) {
		return 0, os.ErrProcessDone
	}
	if err != nil {
		return 0, err
	}
	return p.Proc.P_starttime.Sec, nil
}
