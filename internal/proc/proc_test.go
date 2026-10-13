package proc

import (
	"testing"
	"time"
)

const ps = `    1     0 ??         10-02:03:04 /sbin/launchd
  200     1 ttys001       01:02:03 -fish
  201   200 ttys001          00:05 /opt/bin/claude --resume abc
  202   201 ttys001          00:01 /bin/zsh -c ym item add
  203   202 ttys001          00:01 ym item add
  bad line
  300   303 ??             00:09 loop
  303   300 ??             00:09 loop
`

func TestParseReadsTTYAndElapsed(t *testing.T) {
	procs := Parse(ps)
	if len(procs) != 7 {
		t.Fatalf("parsed %d processes", len(procs))
	}
	if p := procs[0]; p.TTY != "" || p.Elapsed != 10*24*time.Hour+2*time.Hour+3*time.Minute+4*time.Second {
		t.Errorf("launchd = %+v", p)
	}
	if p := procs[2]; p.TTY != "/dev/ttys001" || p.Command() != "claude" || p.Elapsed != 5*time.Second {
		t.Errorf("claude = %+v", p)
	}
	if got := (Proc{Args: []string{"/usr/bin/node", "/lib/codex/bin/codex"}}).Command(); got != "codex" {
		t.Errorf("node launcher command = %q", got)
	}
}

func TestAncestorsNearestFirstAndStopsOnCycles(t *testing.T) {
	table := Index(Parse(ps))
	var pids []int
	for _, p := range table.Ancestors(203) {
		pids = append(pids, p.PID)
	}
	if len(pids) != 3 || pids[0] != 202 || pids[1] != 201 || pids[2] != 200 {
		t.Fatalf("ancestors = %v", pids)
	}
	if got := table.Ancestors(300); len(got) != 1 || got[0].PID != 303 {
		t.Fatalf("cycle ancestors = %+v", got)
	}
	if got := table.Ancestors(999); len(got) != 0 {
		t.Fatalf("missing pid ancestors = %+v", got)
	}
}
