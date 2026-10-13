// Package proc reads the process table.
package proc

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Proc struct {
	PID     int
	PPID    int
	TTY     string
	Elapsed time.Duration
	Args    []string
}

// Started is when the process began, measured back from now.
func (p Proc) Started(now time.Time) time.Time { return now.Add(-p.Elapsed) }

// Command is the program name, looking past a node or bun launcher.
func (p Proc) Command() string {
	if len(p.Args) == 0 {
		return ""
	}
	base := filepath.Base(p.Args[0])
	if (base == "node" || base == "bun") && len(p.Args) > 1 {
		return filepath.Base(p.Args[1])
	}
	return base
}

// List runs ps once and returns every process.
func List() ([]Proc, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,tty=,etime=,args=").Output()
	if err != nil {
		return nil, err
	}
	return Parse(string(out)), nil
}

// Parse reads ps output in pid, ppid, tty, etime, args order.
func Parse(out string) []Proc {
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, TTY: devTTY(f[2]), Elapsed: parseEtime(f[3]), Args: f[4:]})
	}
	return procs
}

func devTTY(t string) string {
	if t == "" || t == "??" || t == "-" {
		return ""
	}
	if strings.HasPrefix(t, "/dev/") {
		return t
	}
	return "/dev/" + t
}

// parseEtime reads ps's [[dd-]hh:]mm:ss elapsed time.
func parseEtime(s string) time.Duration {
	var days int
	if i := strings.IndexByte(s, '-'); i >= 0 {
		days, _ = strconv.Atoi(s[:i])
		s = s[i+1:]
	}
	total := 0
	for _, part := range strings.Split(s, ":") {
		n, _ := strconv.Atoi(part)
		total = total*60 + n
	}
	return time.Duration(days)*24*time.Hour + time.Duration(total)*time.Second
}

// Table indexes processes by pid for ancestry walks.
type Table map[int]Proc

func Index(procs []Proc) Table {
	t := make(Table, len(procs))
	for _, p := range procs {
		t[p.PID] = p
	}
	return t
}

// Ancestors lists pid's parents from nearest to the root, stopping at a cycle or a missing process.
func (t Table) Ancestors(pid int) []Proc {
	var out []Proc
	seen := map[int]bool{pid: true}
	p, ok := t[pid]
	for ok && p.PPID > 1 && !seen[p.PPID] {
		seen[p.PPID] = true
		p, ok = t[p.PPID]
		if ok {
			out = append(out, p)
		}
	}
	return out
}
