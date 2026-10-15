package proc

import (
	"bytes"
	"encoding/binary"
	"os"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// List reads the process table with sysctl, which costs a few milliseconds where ps costs a hundred.
func List() ([]Proc, error) {
	ks, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return listPS()
	}
	ttys := ttyNames()
	now := time.Now()
	procs := make([]Proc, 0, len(ks))
	for _, k := range ks {
		pid := int(k.Proc.P_pid)
		args := procArgs(pid)
		if len(args) == 0 {
			args = []string{unix.ByteSliceToString(k.Proc.P_comm[:])}
		}
		started := time.Unix(k.Proc.P_starttime.Sec, int64(k.Proc.P_starttime.Usec)*1000)
		procs = append(procs, Proc{PID: pid, PPID: int(k.Eproc.Ppid), TTY: ttys[k.Eproc.Tdev], Elapsed: now.Sub(started), Args: args})
	}
	return procs, nil
}

// procArgs reads kern.procargs2: argc, the executable path, padding, then argc NUL-terminated arguments.
func procArgs(pid int) []string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return nil
	}
	argc := int(binary.LittleEndian.Uint32(b))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil
	}
	b = b[i:]
	for len(b) > 0 && b[0] == 0 {
		b = b[1:]
	}
	args := make([]string, 0, argc)
	for len(args) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			j = len(b)
		}
		args = append(args, string(b[:j]))
		b = b[min(j+1, len(b)):]
	}
	return args
}

var (
	ttyOnce sync.Mutex
	ttyMap  map[int32]string
	ttyAt   time.Time
)

// ttyNames maps terminal device numbers to their /dev paths.
func ttyNames() map[int32]string {
	ttyOnce.Lock()
	defer ttyOnce.Unlock()
	if ttyMap != nil && time.Since(ttyAt) < 10*time.Second {
		return ttyMap
	}
	m := map[int32]string{}
	entries, _ := os.ReadDir("/dev")
	for _, e := range entries {
		name := e.Name()
		if len(name) < 4 || name[:3] != "tty" {
			continue
		}
		var st syscall.Stat_t
		if syscall.Stat("/dev/"+name, &st) == nil {
			m[st.Rdev] = "/dev/" + name
		}
	}
	ttyMap, ttyAt = m, time.Now()
	return m
}
