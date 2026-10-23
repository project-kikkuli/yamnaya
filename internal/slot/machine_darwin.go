package slot

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func resources() (int, uint64, error) {
	cores, err := unix.SysctlUint32("hw.physicalcpu")
	if err != nil {
		return 0, 0, err
	}
	memory, err := unix.SysctlUint64("hw.memsize")
	return int(cores), memory, err
}

// Load is the one-minute load average.
func Load() (float64, error) {
	raw, err := unix.SysctlRaw("vm.loadavg")
	if err != nil {
		return 0, err
	}
	if len(raw) < 24 {
		return 0, errors.New("vm.loadavg: short read")
	}
	scale := int64(binary.LittleEndian.Uint64(raw[16:]))
	if scale == 0 {
		return 0, errors.New("vm.loadavg: zero scale")
	}
	return float64(binary.LittleEndian.Uint32(raw)) / float64(scale), nil
}
