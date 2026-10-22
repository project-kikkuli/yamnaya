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
