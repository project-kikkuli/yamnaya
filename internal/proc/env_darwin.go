package proc

import (
	"bytes"
	"encoding/binary"
	"golang.org/x/sys/unix"
)

func ConfigRoot(pid int, key string) string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return ""
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return ""
	}
	b = b[i:]
	b = bytes.TrimLeft(b, "\x00")
	for range argc {
		i = bytes.IndexByte(b, 0)
		if i < 0 {
			return ""
		}
		b = b[i+1:]
	}
	return configValue(b, key)
}
