//go:build !darwin && !linux

package slot

import (
	"errors"
	"runtime"
)

func resources() (int, uint64, error) {
	return 0, 0, errors.New("slots are not supported on " + runtime.GOOS)
}

// Load is the one-minute load average.
func Load() (float64, error) { return 0, errors.New("load average is not read on " + runtime.GOOS) }
