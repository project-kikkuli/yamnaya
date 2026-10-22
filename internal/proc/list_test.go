package proc

import (
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestListSeesProcessesStartAndExitBetweenReads(t *testing.T) {
	find := func(pid int) (Proc, bool) {
		procs, err := List()
		if err != nil {
			t.Fatal(err)
		}
		i := slices.IndexFunc(procs, func(p Proc) bool { return p.PID == pid })
		if i < 0 {
			return Proc{}, false
		}
		return procs[i], true
	}
	if _, err := List(); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sleep", "30", "list-test-marker")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for p, ok := find(cmd.Process.Pid); !ok || !slices.Contains(p.Args, "list-test-marker"); p, ok = find(cmd.Process.Pid) {
		if time.Now().After(deadline) {
			t.Fatalf("new process = %+v, %v", p, ok)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if p, ok := find(cmd.Process.Pid); !ok || !slices.Contains(p.Args, "list-test-marker") {
		t.Fatalf("same process on the next read = %+v, %v", p, ok)
	}
	cmd.Process.Kill()
	cmd.Wait()
	if p, ok := find(cmd.Process.Pid); ok {
		t.Fatalf("exited process still listed: %+v", p)
	}
}
