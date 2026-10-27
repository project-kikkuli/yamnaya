package core

import (
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

func TestTheOwnersYmNeverSeesASessionATestStarts(t *testing.T) {
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	agent := exec.Command("sleep", "30")
	agent.Dir = t.TempDir()
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { agent.Process.Kill(); agent.Wait() })
	cwd := ""
	for end := time.Now().Add(5 * time.Second); cwd == "" && time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		cwd = proc.Cwd(agent.Process.Pid)
	}
	for _, c := range []struct {
		name  string
		state string
		seen  bool
	}{
		{"the owner's ym", filepath.Join(owner.HomeDir, ".local", "state", "yamnaya"), false},
		{"the test's own ym", os.Getenv("YAMNAYA_STATE_DIR"), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.core.StateDir = c.state
			w.run(agent.Process.Pid, 1, "/dev/t1", "claude", "s1", Idle)
			w.claude.sessions[agent.Process.Pid] = Session{ID: "s1", Status: Idle, Cwd: cwd}
			if got := len(w.board(t).Sessions) == 1; got != c.seen {
				t.Fatalf("board on %s shows the session in %s %v, want %v", c.state, cwd, got, c.seen)
			}
		})
	}
}

func TestStopWithoutASignalOfItsOwnSignalsThroughProcSignal(t *testing.T) {
	var sent []int
	old := proc.Signal
	proc.Signal = func(pid int, _ syscall.Signal) error { sent = append(sent, pid); return nil }
	t.Cleanup(func() { proc.Signal = old })
	h := &fakeHarness{name: "claude", sessions: map[int]Session{4242: {ID: "s1"}}}
	c := &Core{Harnesses: []Harness{h}, KillGrace: time.Millisecond,
		Procs: func() ([]proc.Proc, error) { return []proc.Proc{{PID: 4242, Args: []string{"claude"}}}, nil }}
	c.Stop(Live{Session: Session{Harness: "claude", ID: "s1", PID: 4242}})
	if len(sent) != 1 || sent[0] != 4242 {
		t.Fatalf("signalled %v through proc.Signal, want [4242]", sent)
	}
}

func TestADrillsYmSeesOnlySessionsInsideItsOwnRootOrItsLaunches(t *testing.T) {
	base, _ := filepath.EvalSymlinks(t.TempDir())
	drill := filepath.Join(base, "drill")
	inside, outside, launched := filepath.Join(drill, "repo"), filepath.Join(base, "real-repo"), filepath.Join(base, "real-repo-wt-x")
	for _, d := range []string{inside, outside, launched, filepath.Join(drill, "state")} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.WriteFile(filepath.Join(drill, RehearsalMark), nil, 0o644))
	home, _ := os.UserHomeDir()
	owner := filepath.Join(home, ".local", "state", "yamnaya")
	start := func(dir string) string {
		p := exec.Command("sleep", "30")
		p.Dir = dir
		must(t, p.Start())
		t.Cleanup(func() { p.Process.Kill(); p.Wait() })
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			if cwd := proc.Cwd(p.Process.Pid); cwd != "" {
				return cwd
			}
		}
		t.Fatalf("no cwd for a process in %s", dir)
		return ""
	}
	cwds := map[string]string{inside: start(inside), outside: start(outside), launched: start(launched)}
	for _, c := range []struct {
		name  string
		state string
		cwd   string
		seen  bool
	}{
		{"drill ym, session inside its root", filepath.Join(drill, "state"), inside, true},
		{"drill ym, the owner's session outside its root", filepath.Join(drill, "state"), outside, false},
		{"drill ym, a worktree its own launch made outside its root", filepath.Join(drill, "state"), launched, true},
		{"owner's ym, its session outside every drill", owner, outside, true},
		{"owner's ym, the drill's session", owner, inside, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.core.StateDir, w.core.Scoped = c.state, c.state != owner
			w.core.Worktrees = func() []string { return []string{launched} }
			w.run(10, 1, "/dev/t1", "claude", "s1", Idle)
			w.claude.sessions[10] = Session{ID: "s1", Status: Idle, Cwd: cwds[c.cwd]}
			if got := len(w.board(t).Sessions) == 1; got != c.seen {
				t.Fatalf("board on %s shows the session in %s %v, want %v", c.state, cwds[c.cwd], got, c.seen)
			}
			if _, err := w.core.Find("s1"); (err == nil) != c.seen {
				t.Fatalf("Find found it %v, want %v", err == nil, c.seen)
			}
		})
	}
}
