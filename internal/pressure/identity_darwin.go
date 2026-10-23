package pressure

import (
	"bytes"
	"encoding/binary"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/proc"
	"golang.org/x/sys/unix"
)

func ReadIdentity(pid int) Identity {
	raw, _ := unix.SysctlRaw("kern.procargs2", pid)
	return Identity{Thread: threadFromArgs(raw), Cwd: proc.Cwd(pid)}
}

func threadFromArgs(raw []byte) string {
	if len(raw) < 4 {
		return ""
	}
	argc := int(binary.LittleEndian.Uint32(raw))
	if argc < 1 {
		return ""
	}
	raw = raw[4:]
	end := bytes.IndexByte(raw, 0)
	if end < 0 {
		return ""
	}
	raw = bytes.TrimLeft(raw[end:], "\x00")
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(raw, 0)
		if end < 0 {
			return ""
		}
		raw = raw[end+1:]
	}
	for _, env := range bytes.Split(raw, []byte{0}) {
		if value, ok := strings.CutPrefix(string(env), "CODEX_THREAD_ID="); ok {
			return value
		}
	}
	return ""
}
