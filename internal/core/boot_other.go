//go:build !darwin

package core

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func bootTime() time.Time {
	raw, err := os.ReadFile("/proc/stat")
	if err != nil {
		return time.Time{}
	}
	for line := range strings.Lines(string(raw)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "btime "); ok {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				return time.Unix(n, 0)
			}
		}
	}
	return time.Time{}
}
