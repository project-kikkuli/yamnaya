//go:build !linux

package proc

import (
	"os/exec"
	"strconv"
	"strings"
)

// Cwd is a process's working directory, or "".
func Cwd(pid int) string {
	if names := lsof(pid, "-d", "cwd"); len(names) > 0 {
		return names[0]
	}
	return ""
}

// Files are the paths a process holds open.
func Files(pid int) []string { return lsof(pid) }

func lsof(pid int, args ...string) []string {
	out, err := exec.Command("lsof", append([]string{"-a", "-Fn", "-p", strconv.Itoa(pid)}, args...)...).Output()
	if err != nil && len(out) == 0 {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n") {
			names = append(names, line[1:])
		}
	}
	return names
}
