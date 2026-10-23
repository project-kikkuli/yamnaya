package core

import (
	"time"

	"golang.org/x/sys/unix"
)

func bootTime() time.Time {
	tv, err := unix.SysctlTimeval("kern.boottime")
	if err != nil {
		return time.Time{}
	}
	return time.Unix(tv.Unix())
}
