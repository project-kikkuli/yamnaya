package pressure

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func Processes(ctx context.Context) ([]Process, error) {
	cmd := exec.CommandContext(ctx, "ps", "-A", "-o", "pid=,ppid=,ni=,pcpu=,lstart=,comm=")
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseProcesses(string(out))
}

func parseProcesses(raw string) ([]Process, error) {
	var out []Process
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		if len(f) < 10 {
			return nil, fmt.Errorf("short process row: %q", line)
		}
		pid, e1 := strconv.Atoi(f[0])
		parent, e2 := strconv.Atoi(f[1])
		nice, e3 := strconv.Atoi(f[2])
		unavailable := f[2] == "-"
		if unavailable {
			nice, e3 = 0, nil
		}
		cpu, e4 := strconv.ParseFloat(f[3], 64)
		start, e5 := time.ParseInLocation("Mon Jan 2 15:04:05 2006", strings.Join(f[4:9], " "), time.Local)
		if err := errors.Join(e1, e2, e3, e4, e5); err != nil {
			return nil, fmt.Errorf("process row: %w", err)
		}
		out = append(out, Process{PID: pid, PPID: parent, Nice: nice, NiceUnavailable: unavailable, CPU: cpu, Started: start.Unix(), Command: strings.Join(f[9:], " ")})
	}
	return out, nil
}

func SetNice(p Process, nice int) error {
	start, err := started(p.PID)
	if err != nil {
		return err
	}
	if start != p.Started {
		return os.ErrProcessDone
	}
	err = syscall.Setpriority(syscall.PRIO_PROCESS, p.PID, nice)
	if errors.Is(err, syscall.ESRCH) {
		return os.ErrProcessDone
	}
	return err
}
