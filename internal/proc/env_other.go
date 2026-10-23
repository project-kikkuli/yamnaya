//go:build !darwin && !linux

package proc

func ConfigRoot(pid int, key string) string { return "" }
