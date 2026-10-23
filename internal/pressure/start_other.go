//go:build !darwin

package pressure

import (
	"context"
	"os"
	"os/exec"
	"strconv"
)

func started(pid int) (int64, error) {
	out, err := exec.CommandContext(context.Background(), "ps", "-p", strconv.Itoa(pid), "-o", "pid=,ppid=,ni=,pcpu=,lstart=,comm=").Output()
	if err != nil {
		return 0, os.ErrProcessDone
	}
	ps, err := parseProcesses(string(out))
	if err != nil {
		return 0, err
	}
	if len(ps) != 1 {
		return 0, os.ErrProcessDone
	}
	return ps[0].Started, nil
}
