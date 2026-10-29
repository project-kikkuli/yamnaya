package pressure

import (
	"errors"
	"os"
	"strconv"
	"strings"
)

func memory() (Host, error) {
	var h Host
	raw, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return h, err
	}
	kib := map[string]uint64{}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 {
			n, _ := strconv.ParseUint(f[1], 10, 64)
			kib[strings.TrimSuffix(f[0], ":")] = n * 1024
		}
	}
	h.Memory, h.Free, h.Swap = kib["MemTotal"], kib["MemAvailable"], kib["SwapTotal"]-kib["SwapFree"]
	if psi, err := os.ReadFile("/proc/pressure/memory"); err == nil {
		h.Level = psiLevel(string(psi))
	}
	return h, nil
}

// psiLevel maps memory stall averages onto the darwin levels: warn when some task stalled 10% of the last 10s, critical
// when every task did.
func psiLevel(raw string) int {
	level := 1
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 2 {
			continue
		}
		avg, _ := strconv.ParseFloat(strings.TrimPrefix(f[1], "avg10="), 64)
		switch {
		case f[0] == "full" && avg >= 10:
			level = max(level, 4)
		case f[0] == "some" && avg >= 10:
			level = max(level, 2)
		}
	}
	return level
}

func ticks() (busy, total uint64, err error) {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return 0, 0, err
	}
	line, _, _ := strings.Cut(string(raw), "\n")
	f := strings.Fields(line)
	if len(f) < 6 || f[0] != "cpu" {
		return 0, 0, errors.New("/proc/stat: no cpu row")
	}
	for i, field := range f[1:] {
		n, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return 0, 0, err
		}
		total += n
		if i != 3 && i != 4 {
			busy += n
		}
	}
	return busy, total, nil
}
