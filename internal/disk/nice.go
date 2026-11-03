package disk

import (
	"os"
	"strconv"

	"golang.org/x/sys/unix"
)

// Lower runs this process at the lowest priority from now on. On Linux niceness is per thread, so it renices each of them;
// a thread starts at its creator's niceness.
func Lower() {
	unix.Setpriority(unix.PRIO_PROCESS, 0, 19)
	tasks, err := os.ReadDir("/proc/self/task")
	if err != nil {
		return
	}
	for _, t := range tasks {
		if tid, err := strconv.Atoi(t.Name()); err == nil {
			unix.Setpriority(unix.PRIO_PROCESS, tid, 19)
		}
	}
}
