package proc

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// clockTicks is USER_HZ, which Linux fixes at 100 on every architecture Go targets.
const clockTicks = 100

func readProc(root string) ([]Proc, error) {
	up, err := os.ReadFile(filepath.Join(root, "uptime"))
	if err != nil {
		return nil, err
	}
	f := strings.Fields(string(up))
	if len(f) == 0 {
		return nil, os.ErrInvalid
	}
	uptime, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	procs := make([]Proc, 0, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(root, e.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue
		}
		p, ok := parseStat(pid, string(stat), uptime)
		if !ok {
			continue
		}
		if cmd, err := os.ReadFile(filepath.Join(dir, "cmdline")); err == nil && len(cmd) > 0 {
			p.Args = strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00")
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// parseStat reads /proc/<pid>/stat: pid (comm) state ppid pgrp session tty_nr ... starttime is field 22.
func parseStat(pid int, stat string, uptime float64) (Proc, bool) {
	open, end := strings.IndexByte(stat, '('), strings.LastIndexByte(stat, ')')
	if open < 0 || end < open {
		return Proc{}, false
	}
	f := strings.Fields(stat[end+1:])
	if len(f) < 20 {
		return Proc{}, false
	}
	ppid, err1 := strconv.Atoi(f[1])
	tty, err2 := strconv.ParseInt(f[4], 10, 64)
	start, err3 := strconv.ParseFloat(f[19], 64)
	if err1 != nil || err2 != nil || err3 != nil {
		return Proc{}, false
	}
	elapsed := time.Duration((uptime - start/clockTicks) * float64(time.Second)).Round(10 * time.Millisecond)
	return Proc{PID: pid, PPID: ppid, TTY: ttyName(tty), Elapsed: max(elapsed, 0), Args: []string{stat[open+1 : end]}}, true
}

// ttyName names a tty_nr device: majors 136-143 are /dev/pts, 4 is the virtual consoles.
func ttyName(dev int64) string {
	if dev == 0 {
		return ""
	}
	major, minor := (dev>>8)&0xfff, (dev&0xff)|((dev>>12)&0xfff00)
	switch {
	case major >= 136 && major <= 143:
		return "/dev/pts/" + strconv.FormatInt((major-136)*256+minor, 10)
	case major == 4 && minor < 64:
		return "/dev/tty" + strconv.FormatInt(minor, 10)
	case major == 4:
		return "/dev/ttyS" + strconv.FormatInt(minor-64, 10)
	}
	return ""
}
