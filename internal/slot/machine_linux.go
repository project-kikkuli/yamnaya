package slot

import (
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

func resources() (int, uint64, error) {
	var info unix.Sysinfo_t
	if err := unix.Sysinfo(&info); err != nil {
		return 0, 0, err
	}
	return physicalCores(), uint64(info.Totalram) * uint64(info.Unit), nil
}

// physicalCores counts distinct package and core pairs, falling back to logical CPUs.
func physicalCores() int {
	cpus, _ := filepath.Glob("/sys/devices/system/cpu/cpu[0-9]*")
	seen := map[string]bool{}
	for _, cpu := range cpus {
		pkg, err1 := os.ReadFile(filepath.Join(cpu, "topology", "physical_package_id"))
		core, err2 := os.ReadFile(filepath.Join(cpu, "topology", "core_id"))
		if err1 == nil && err2 == nil {
			seen[strings.TrimSpace(string(pkg))+"/"+strings.TrimSpace(string(core))] = true
		}
	}
	if len(seen) == 0 {
		return runtime.NumCPU()
	}
	return len(seen)
}

// Load is the one-minute load average.
func Load() (float64, error) {
	raw, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(strings.Fields(string(raw))[0], 64)
}
