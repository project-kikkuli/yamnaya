package slot

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestCapacityLeavesTheMachineRoomAndTakesTheScarcerOfCoresAndMemory(t *testing.T) {
	for _, c := range []struct {
		cores  int
		memory uint64
		want   int
	}{
		{12, 24 * gib, 4},
		{8, 16 * gib, 3},
		{64, 16 * gib, 3},
		{16, 128 * gib, 7},
		{2, 8 * gib, 1},
		{1, 2 * gib, 1},
	} {
		if got := Capacity(c.cores, c.memory); got != c.want {
			t.Errorf("Capacity(%d cores, %d GiB) = %d, want %d", c.cores, c.memory/gib, got, c.want)
		}
	}
}

func pool(t *testing.T) Pool {
	return Pool{Dir: t.TempDir(), Capacity: 1, Cores: 4, Poll: 10 * time.Millisecond}
}

func TestAWaiterIsListedAndTakesTheSlotItsHolderGivesBack(t *testing.T) {
	p := pool(t)
	release, n, err := p.Acquire(context.Background(), Claim{PID: os.Getpid(), Command: []string{"go", "test"}}, nil)
	if err != nil || n != 1 {
		t.Fatalf("first acquire: slot %d, %v", n, err)
	}
	waiting := make(chan struct{})
	got := make(chan int, 1)
	go func() {
		rel, n, err := p.Acquire(context.Background(), Claim{PID: os.Getpid(), Command: []string{"make"}, Since: time.Now()}, func() { close(waiting) })
		if err != nil {
			t.Error(err)
		}
		rel()
		got <- n
	}()
	<-waiting
	st, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Holders) != 1 || st.Holders[0].Line() != "go test" || len(st.Waiters) != 1 || st.Waiters[0].Line() != "make" {
		t.Fatalf("status = %+v, want go test holding and make waiting", st)
	}
	release()
	if n := <-got; n != 1 {
		t.Fatalf("waiter got slot %d, want 1", n)
	}
	if st, _ = p.Status(); len(st.Holders) != 0 || len(st.Waiters) != 0 {
		t.Fatalf("status after both released = %+v, want an empty pool", st)
	}
}

func TestASlotHeldWithoutAClaimShowsAsAgentSlot(t *testing.T) {
	p := pool(t)
	f, err := os.OpenFile(filepath.Join(p.Dir, "1.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	st, err := p.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Holders) != 1 || st.Holders[0].Line() != "agent-slot (no details)" {
		t.Fatalf("holders = %+v, want one agent-slot holder", st.Holders)
	}
}

func TestACanceledWaiterLeavesTheWaitingList(t *testing.T) {
	p := pool(t)
	release, _, err := p.Acquire(context.Background(), Claim{PID: os.Getpid()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	_, _, err = p.Acquire(ctx, Claim{PID: os.Getpid()}, cancel)
	if err != context.Canceled {
		t.Fatalf("err = %v, want canceled", err)
	}
	if st, _ := p.Status(); len(st.Waiters) != 0 {
		t.Fatalf("waiters = %+v, want none", st.Waiters)
	}
}

func TestRunPassesTheExitCodeOnAndEndsWhatTheCommandLeftRunning(t *testing.T) {
	Grace = 200 * time.Millisecond
	pidFile := filepath.Join(t.TempDir(), "pid")
	for _, c := range []struct {
		script string
		code   int
	}{
		{"exit 7", 7},
		{"sleep 30 & echo $! > " + pidFile, 0},
	} {
		code, err := Run([]string{"sh", "-c", c.script}, 0, nil)
		if err != nil || code != c.code {
			t.Fatalf("%q: code %d, err %v; want %d", c.script, code, err, c.code)
		}
	}
	raw, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
	if alive(pid) {
		syscall.Kill(pid, syscall.SIGKILL)
		t.Fatalf("the backgrounded sleep %d outlived its command", pid)
	}
}

func TestParseCPUReadsPsLines(t *testing.T) {
	got := ParseCPU("  101  12.5\n 7 0.0\nbad line here\n")
	if got[101] != 12.5 || got[7] != 0 || len(got) != 2 {
		t.Fatalf("ParseCPU = %v", got)
	}
}
