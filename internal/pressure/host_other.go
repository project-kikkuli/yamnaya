//go:build !darwin && !linux

package pressure

import (
	"errors"
	"runtime"
)

func memory() (Host, error) {
	return Host{}, errors.New("memory pressure is not read on " + runtime.GOOS)
}
