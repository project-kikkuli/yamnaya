package agenttest

import (
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"
)

func TestMain(m *testing.M) { os.Exit(Run(m)) }

func TestATestProcessLivesInsideARehearsalRootAwayFromTheOwnersFiles(t *testing.T) {
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	root := filepath.Dir(home)
	if _, err := os.Stat(filepath.Join(root, ".ym-rehearsal")); err != nil {
		t.Fatalf("%s is not a rehearsal root: %v", root, err)
	}
	for _, env := range []string{"HOME", "TMPDIR", "YAMNAYA_STATE_DIR", "CLAUDE_CONFIG_DIR", "CODEX_HOME"} {
		v := os.Getenv(env)
		if !strings.HasPrefix(v, root+string(filepath.Separator)) || strings.HasPrefix(v, owner.HomeDir+string(filepath.Separator)) {
			t.Errorf("%s = %q, want a directory under %s", env, v, root)
		}
	}
	for _, env := range []string{"GOMODCACHE", "GOCACHE"} {
		if v := os.Getenv(env); v == "" || strings.HasPrefix(v, root+string(filepath.Separator)) {
			t.Errorf("%s = %q, want the owner's cache outside %s, so a go command a test runs downloads nothing into it", env, v, root)
		}
	}
	if dir := t.TempDir(); !strings.HasPrefix(dir, root+string(filepath.Separator)) {
		t.Errorf("t.TempDir() = %s, outside %s", dir, root)
	}
	for _, env := range []string{"YAMNAYA_YM_SOCKET", "YAMNAYA_TMUX_SOCKET"} {
		if v := os.Getenv(env); !strings.HasPrefix(v, "ymt-") {
			t.Errorf("%s = %q, want a private tmux server", env, v)
		}
	}
	for _, env := range []string{"TMUX", "TMUX_PANE", "YAMNAYA_PROFILE"} {
		if v, ok := os.LookupEnv(env); ok {
			t.Errorf("%s = %q leaks the calling agent's session into the test", env, v)
		}
	}
}

func TestASignalReachesOnlyAProcessTheTestStartedOrOneWorkingInsideItsRoot(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { child.Process.Kill(); child.Wait() })
	sock := tmuxtest.Socket(t)
	if out, err := exec.Command("tmux", "-L", sock, "-f", "/dev/null", "new-session", "-d", "-c", root, "sleep", "30").CombinedOutput(); err != nil {
		t.Fatalf("tmux: %v: %s", err, out)
	}
	pane := 0
	for end := time.Now().Add(5 * time.Second); pane == 0 && time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		out, _ := exec.Command("tmux", "-L", sock, "display-message", "-p", "#{pane_pid}").Output()
		pane = atoi(strings.TrimSpace(string(out)))
	}
	for _, c := range []struct {
		name string
		pid  int
		lets bool
	}{
		{"a process the test started", child.Process.Pid, true},
		{"a tmux pane working inside the root, outside the test's process tree", pane, true},
		{"the go command that runs the test", os.Getppid(), false},
		{"launchd", 1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &fence{root: root}
			err := f.signal(c.pid, 0)
			if lets := err == nil; lets != c.lets {
				t.Fatalf("signal to pid %d let through %v (%v), want %v", c.pid, lets, err, c.lets)
			}
			if !c.lets && (!errors.Is(err, syscall.EPERM) || len(f.refused()) != 1) {
				t.Fatalf("refusal %v recorded %q, want EPERM recorded once", err, f.refused())
			}
		})
	}
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}
