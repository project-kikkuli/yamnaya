package pressure

import (
	"encoding/binary"
	"errors"

	"golang.org/x/sys/unix"
)

func memory() (Host, error) {
	var h Host
	var err error
	if h.Memory, err = unix.SysctlUint64("hw.memsize"); err != nil {
		return h, err
	}
	free, err := unix.SysctlUint32("vm.page_free_count")
	if err != nil {
		return h, err
	}
	h.Free = uint64(free) * uint64(unix.Getpagesize())
	level, err := unix.SysctlUint32("kern.memorystatus_vm_pressure_level")
	if err != nil {
		return h, err
	}
	h.Level = int(level)
	// struct xsw_usage: total, avail, used.
	raw, err := unix.SysctlRaw("vm.swapusage")
	if err != nil {
		return h, err
	}
	if len(raw) < 24 {
		return h, errors.New("vm.swapusage: short read")
	}
	h.Swap = binary.LittleEndian.Uint64(raw[16:])
	return h, nil
}
