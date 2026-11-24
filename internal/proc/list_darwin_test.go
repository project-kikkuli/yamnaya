package proc

import (
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestListSeesTheProgramASettledProcessExecs(t *testing.T) {
	settle := argsSettle
	argsSettle = 0
	t.Cleanup(func() { argsSettle = settle })
	cmd := exec.Command("sh", "-c", "read _; exec sleep 30", "before-exec")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	args := func() []string {
		procs, err := List()
		if err != nil {
			t.Fatal(err)
		}
		if i := slices.IndexFunc(procs, func(p Proc) bool { return p.PID == cmd.Process.Pid }); i >= 0 {
			return procs[i].Args
		}
		return nil
	}
	deadline := time.Now().Add(5 * time.Second)
	for !slices.Contains(args(), "before-exec") {
		if time.Now().After(deadline) {
			t.Fatalf("args before exec = %q", args())
		}
		time.Sleep(10 * time.Millisecond)
	}
	in.Write([]byte("\n"))
	for a := args(); !slices.Equal(a, []string{"sleep", "30"}); a = args() {
		if time.Now().After(deadline) {
			t.Fatalf("args after exec = %q, want the program it became", a)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestATerminalOpenedSinceTheLastReadIsNamedAtOnce(t *testing.T) {
	for _, c := range []struct {
		name      string
		devs      []map[int32]string
		lookups   []int32
		want      string
		wantScans int
	}{
		{"a known terminal reads /dev once", []map[int32]string{{4: "/dev/ttys004"}}, []int32{4, 4}, "/dev/ttys004", 1},
		{"a terminal opened after the read is named", []map[int32]string{{4: "/dev/ttys004"}, {4: "/dev/ttys004", 5: "/dev/ttys005"}}, []int32{4, 5}, "/dev/ttys005", 2},
		{"a device with no terminal reads /dev again only once", []map[int32]string{{4: "/dev/ttys004"}}, []int32{-1, -1, -1}, "", 2},
	} {
		t.Run(c.name, func(t *testing.T) {
			scans := 0
			cache := ttyCache{scan: func() map[int32]string {
				m := c.devs[min(scans, len(c.devs)-1)]
				scans++
				return m
			}}
			now := time.Now()
			var got string
			for _, dev := range c.lookups {
				got = cache.name(dev, now)
			}
			if got != c.want || scans != c.wantScans {
				t.Fatalf("name %q after %d reads of /dev, want %q after %d", got, scans, c.want, c.wantScans)
			}
		})
	}
}

func TestAnArgumentReadTheKernelRefusesKeepsWhatTheSameProcessGave(t *testing.T) {
	start := time.Now().Add(-time.Second)
	claude := argsEntry{started: start, comm: "bash", args: []string{"claude", "--resume", "x"}, read: true}
	for _, c := range []struct {
		name    string
		prev    argsEntry
		cached  bool
		started time.Time
		comm    string
		read    []string
		want    []string
	}{
		{"a refused read keeps the process's arguments", claude, true, start, "bash", nil, claude.args},
		{"a fresh read replaces them", claude, true, start, "bash", []string{"claude", "-c"}, []string{"claude", "-c"}},
		{"a process never read stands in as its name", argsEntry{}, false, start, "bash", nil, []string{"bash"}},
		{"a reused pid does not inherit them", claude, true, start.Add(time.Millisecond), "bash", nil, []string{"bash"}},
		{"a process that exec'd does not keep them", claude, true, start, "sleep", nil, []string{"sleep"}},
		{"a stand-in is read again", argsEntry{started: start, comm: "bash", args: []string{"bash"}}, true, start, "bash", []string{"claude"}, []string{"claude"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := nextArgs(c.prev, c.cached, c.started, c.comm, time.Now(), func() []string { return c.read })
			if !slices.Equal(got.args, c.want) {
				t.Fatalf("args %q, want %q", got.args, c.want)
			}
		})
	}
}
