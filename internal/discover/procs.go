package discover

import (
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
)

type Proc struct {
	PID     int
	PPID    int
	Elapsed time.Duration
	Args    []string
}

func PS() ([]Proc, error) {
	out, err := exec.Command("ps", "-A", "-o", "pid=,ppid=,etime=,args=").Output()
	if err != nil {
		return nil, err
	}
	return ParsePS(string(out)), nil
}

func ParsePS(out string) []Proc {
	var procs []Proc
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) < 4 {
			continue
		}
		pid, err1 := strconv.Atoi(f[0])
		ppid, err2 := strconv.Atoi(f[1])
		if err1 != nil || err2 != nil {
			continue
		}
		procs = append(procs, Proc{PID: pid, PPID: ppid, Elapsed: parseEtime(f[2]), Args: f[3:]})
	}
	return procs
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

func harnessOf(p Proc) string {
	for i, a := range p.Args {
		if i > 1 || (i == 1 && filepath.Base(p.Args[0]) != "node") {
			break
		}
		switch filepath.Base(a) {
		case "claude":
			return harness.Claude
		case "codex":
			return harness.Codex
		}
	}
	return ""
}

type found struct {
	pane    tmux.PaneInfo
	proc    Proc
	harness string
}

// harnessProcs finds the outermost harness process under each live pane.
func harnessProcs(panes []tmux.PaneInfo, procs []Proc) []found {
	children := map[int][]Proc{}
	byPID := map[int]Proc{}
	for _, p := range procs {
		children[p.PPID] = append(children[p.PPID], p)
		byPID[p.PID] = p
	}
	var out []found
	for _, pane := range panes {
		root, ok := byPID[pane.PID]
		if pane.Dead || !ok {
			continue
		}
		level := []Proc{root}
		for len(level) > 0 {
			var hit *found
			var next []Proc
			for _, p := range level {
				if h := harnessOf(p); h != "" && hit == nil {
					hit = &found{pane: pane, proc: p, harness: h}
				}
				next = append(next, children[p.PID]...)
			}
			if hit != nil {
				out = append(out, *hit)
				break
			}
			level = next
		}
	}
	return out
}

// LsofCwds reads each process's working directory.
func LsofCwds(pids []int) map[int]string {
	ids := make([]string, len(pids))
	for i, p := range pids {
		ids[i] = strconv.Itoa(p)
	}
	out, _ := exec.Command("lsof", "-a", "-d", "cwd", "-Fn", "-p", strings.Join(ids, ",")).Output()
	cwds := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "p"):
			pid, _ = strconv.Atoi(line[1:])
		case strings.HasPrefix(line, "n") && pid != 0:
			cwds[pid] = line[1:]
		}
	}
	return cwds
}
