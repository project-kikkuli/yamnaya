package core

import (
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// child starts a process this test owns and reports whether it still runs.
func child(t *testing.T, script string) (int, func() bool) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		cmd.Process.Kill()
		<-done
	})
	return cmd.Process.Pid, func() bool {
		select {
		case <-done:
			return false
		default:
			return true
		}
	}
}

func TestKillTermsTheAgentAndReportsOneThatOutlivesIt(t *testing.T) {
	for _, c := range []struct {
		name, script, want string
		alive              bool
	}{
		{"ends on SIGTERM", "exec sleep 30", "killed claude s1", false},
		{"ignores SIGTERM", `trap "" TERM; exec sleep 30`, "still running (pid ", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			pid, runs := child(t, c.script)
			time.Sleep(100 * time.Millisecond)
			h := &fakeHarness{name: "claude", sessions: map[int]Session{pid: {ID: "s1"}}}
			core := &Core{Harnesses: []Harness{h}, KillGrace: 500 * time.Millisecond, Procs: func() ([]proc.Proc, error) {
				if !runs() {
					return nil, nil
				}
				return []proc.Proc{{PID: pid, TTY: "/dev/ttys999", Args: []string{"claude"}}}, nil
			}}
			s, err := core.Find("s1")
			if err != nil {
				t.Fatal(err)
			}
			msg, err := core.Kill(s)
			got := msg
			if err != nil {
				got = err.Error()
			}
			if !strings.Contains(got, c.want) || runs() != c.alive {
				t.Fatalf("kill said %q, child alive %v; want %q, alive %v", got, runs(), c.want, c.alive)
			}
		})
	}
}

func TestKillNeverSignalsAPidThatNowRunsSomethingElse(t *testing.T) {
	h := &fakeHarness{name: "claude", sessions: map[int]Session{7: {ID: "other"}}}
	var sent []int
	c := &Core{Harnesses: []Harness{h}, Signal: func(pid int, _ syscall.Signal) error { sent = append(sent, pid); return nil },
		Procs: func() ([]proc.Proc, error) { return []proc.Proc{{PID: 7, Args: []string{"claude"}}}, nil }}
	if _, err := c.Kill(Live{Session: Session{Harness: "claude", ID: "s1", PID: 7}}); err == nil || len(sent) > 0 {
		t.Fatalf("kill of a reused pid: err %v, signalled %v", err, sent)
	}
}
