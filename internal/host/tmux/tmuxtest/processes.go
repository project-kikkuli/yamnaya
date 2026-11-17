package tmuxtest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

type fixtureProcess struct {
	pid, parent int
	identity    string
	zombie      bool
}

func fixtureProcesses(args ...string) ([]fixtureProcess, error) {
	args = append(args, "-o", "pid=,ppid=,lstart=,stat=")
	out, err := exec.Command("ps", args...).Output()
	var exit *exec.ExitError
	if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1 && len(out) == 0) {
		return nil, err
	}
	var processes []fixtureProcess
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 8 {
			continue
		}
		pid, pidErr := strconv.Atoi(fields[0])
		parent, parentErr := strconv.Atoi(fields[1])
		if pidErr != nil || parentErr != nil {
			return nil, fmt.Errorf("invalid fixture process: %q", line)
		}
		identity := strings.Join(fields[2:7], " ")
		if runtime.GOOS == "linux" {
			stat, readErr := os.ReadFile(filepath.Join("/proc", fields[0], "stat"))
			if os.IsNotExist(readErr) {
				continue
			}
			if readErr != nil {
				return nil, readErr
			}
			end := strings.LastIndex(string(stat), ") ")
			if end < 0 {
				return nil, fmt.Errorf("invalid fixture process identity: %d", pid)
			}
			values := strings.Fields(string(stat)[end+2:])
			if len(values) < 20 {
				return nil, fmt.Errorf("invalid fixture process identity: %d", pid)
			}
			identity = values[19]
		}
		processes = append(processes, fixtureProcess{pid: pid, parent: parent, identity: identity, zombie: strings.HasPrefix(fields[7], "Z")})
	}
	return processes, nil
}

func serverProcesses(tmux, socket string) (map[int]string, error) {
	out, err := exec.Command(tmux, "-L", socket, "display-message", "-p", "#{pid}").Output()
	if err != nil {
		return nil, nil
	}
	root, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		return nil, err
	}
	processes, err := fixtureProcesses("-A")
	if err != nil {
		return nil, err
	}
	owned := map[int]string{}
	parents := map[int]bool{root: true}
	for changed := true; changed; {
		changed = false
		for _, process := range processes {
			if !parents[process.pid] && parents[process.parent] {
				parents[process.pid] = true
				changed = true
			}
			if parents[process.pid] {
				owned[process.pid] = process.identity
			}
		}
	}
	return owned, nil
}

func waitServerProcesses(t testing.TB, owned map[int]string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for len(owned) != 0 {
		var pids []string
		for pid := range owned {
			pids = append(pids, strconv.Itoa(pid))
		}
		processes, err := fixtureProcesses("-p", strings.Join(pids, ","))
		if err != nil {
			t.Errorf("wait for private tmux processes: %v", err)
			return
		}
		alive := map[int]string{}
		for _, process := range processes {
			if !process.zombie && owned[process.pid] == process.identity {
				alive[process.pid] = process.identity
			}
		}
		owned = alive
		if len(owned) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("private tmux processes survived server shutdown: %v", owned)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
