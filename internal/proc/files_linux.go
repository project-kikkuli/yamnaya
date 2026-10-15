package proc

import (
	"os"
	"path/filepath"
	"strconv"
)

// Cwd is a process's working directory, or "".
func Cwd(pid int) string {
	dir, _ := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "cwd"))
	return dir
}

// Files are the paths a process holds open.
func Files(pid int) []string {
	fd := filepath.Join("/proc", strconv.Itoa(pid), "fd")
	entries, err := os.ReadDir(fd)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if target, err := os.Readlink(filepath.Join(fd, e.Name())); err == nil && filepath.IsAbs(target) {
			names = append(names, target)
		}
	}
	return names
}
