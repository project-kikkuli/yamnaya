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
	now := time.Now()
	argsMu.Lock()
	defer argsMu.Unlock()
	seen := make(map[int]argsEntry, len(ks))
	procs := make([]Proc, 0, len(ks))
	for _, k := range ks {
		pid := int(k.Proc.P_pid)
		started := time.Unix(k.Proc.P_starttime.Sec, int64(k.Proc.P_starttime.Usec)*1000)
		comm := unix.ByteSliceToString(k.Proc.P_comm[:])
		prev, ok := argsCache[pid]
		e := nextArgs(prev, ok, started, comm, now, func() []string { return procArgs(pid) })
		seen[pid] = e
		procs = append(procs, Proc{PID: pid, PPID: int(k.Eproc.Ppid), TTY: ttys.name(k.Eproc.Tdev, now), Elapsed: now.Sub(started), Args: e.args})
	}
	argsCache = seen
	return procs, nil
}

// argsSettle is how long a new process may still retitle itself; its arguments are read again until it is that old,
// or after it execs another program.
var argsSettle = time.Minute

type argsEntry struct {
	started time.Time
	comm    string
	args    []string
	// read is whether args came from the kernel rather than standing in for arguments it would not give.
	read bool
}

// nextArgs is a process's arguments for this listing. kern.procargs2 sometimes refuses a live process for a moment, so a refusal keeps
// what the same process gave before; a process never read stands in as its command name and is read again next time.
func nextArgs(prev argsEntry, cached bool, started time.Time, comm string, now time.Time, read func() []string) argsEntry {
	same := cached && prev.started.Equal(started) && prev.comm == comm
	if same && prev.read && now.Sub(started) >= argsSettle {
		return prev
	}
	if args := read(); len(args) > 0 {
		return argsEntry{started: started, comm: comm, args: args, read: true}
	}
	if same && prev.read {
		return prev
	}
	return argsEntry{started: started, comm: comm, args: []string{comm}}
}

var (
	argsMu    sync.Mutex
	argsCache map[int]argsEntry
)

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

var ttys = ttyCache{scan: scanTTYs}

// ttyCache maps terminal device numbers to their /dev paths. A device it does not know makes it read /dev again once, so a
// terminal opened since the last read is named at once.
type ttyCache struct {
	mu     sync.Mutex
	names  map[int32]string
	missed map[int32]bool
	at     time.Time
	scan   func() map[int32]string
}

func (c *ttyCache) name(dev int32, now time.Time) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.names == nil || now.Sub(c.at) >= 10*time.Second {
		c.names, c.missed, c.at = c.scan(), map[int32]bool{}, now
	}
	if name, ok := c.names[dev]; ok || c.missed[dev] {
		return name
	}
	c.names, c.at = c.scan(), now
	if _, ok := c.names[dev]; !ok {
		c.missed[dev] = true
	}
	return c.names[dev]
}

func scanTTYs() map[int32]string {
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
	return m
}
