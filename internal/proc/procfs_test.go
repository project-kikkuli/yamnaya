package proc

import (
	"testing"
	"time"
)

func TestReadProcParsesStatAndCmdline(t *testing.T) {
	procs, err := readProc("testdata/proc")
	if err != nil {
		t.Fatal(err)
	}
	table := Index(procs)
	if len(table) != 3 {
		t.Fatalf("read %d processes: %+v", len(table), procs)
	}
	for _, c := range []struct {
		pid     int
		ppid    int
		tty     string
		elapsed time.Duration
		command string
	}{
		{1, 0, "", 12345*time.Second + 620*time.Millisecond, "init"},
		{4242, 4241, "/dev/pts/3", 100 * time.Second, "cli.js"},
		{4243, 1, "/dev/pts/300", time.Second, "tmux: srv) x"},
	} {
		p := table[c.pid]
		if p.PPID != c.ppid || p.TTY != c.tty || p.Elapsed != c.elapsed || p.Command() != c.command {
			t.Errorf("pid %d = %+v, command %q", c.pid, p, p.Command())
		}
	}
	if got := table[4242].Params(); len(got) != 2 || got[0] != "--resume" {
		t.Errorf("claude params = %q", got)
	}
}

func TestTTYName(t *testing.T) {
	for dev, want := range map[int64]string{0: "", 136<<8 | 0: "/dev/pts/0", 4<<8 | 1: "/dev/tty1", 4<<8 | 65: "/dev/ttyS1", 5<<8 | 1: ""} {
		if got := ttyName(dev); got != want {
			t.Errorf("ttyName(%d) = %q, want %q", dev, got, want)
		}
	}
}
