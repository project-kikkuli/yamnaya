//go:build !linux && !(darwin && cgo)

package pressure

// ticks reads nothing without cgo on darwin, so Busy stays 0 and only memory can trip the throttle.
func ticks() (busy, total uint64, err error) { return 0, 0, nil }
