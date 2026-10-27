// Package agenttest keeps a test process, and the claude, codex, tmux and ym it starts, real or fake, out of everything the
// owner's own ym and agents use.
package agenttest

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// Run runs m inside a fresh temporary root, removed afterwards, and returns its exit code. The root is a rehearsal root, so the
// owner's ym never shows, records or adopts a session a test starts. HOME, TMPDIR, CLAUDE_CONFIG_DIR and CODEX_HOME point
// inside it, YAMNAYA_STATE_DIR at the default state dir under that HOME, and ym's tmux servers and brief page are private ones. A signal to a process neither started by the test nor
// working inside the root is refused and fails the run. A test binary a test runs again keeps the root it inherits.
func Run(m *testing.M) int {
	if root := os.Getenv(rootEnv); root != "" {
		f := &fence{root: root}
		proc.Signal = f.signal
		return f.check(m.Run())
	}
	base, err := os.MkdirTemp("", "ymt-")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(base)
	root, err := filepath.EvalSymlinks(base)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".ym-rehearsal"), nil, 0o644); err != nil {
		panic(err)
	}
	os.Setenv(rootEnv, root)
	for _, env := range []string{"CODEX_THREAD_ID", "CLAUDE_CODE_SESSION_ID", "CLAUDE_SECURESTORAGE_CONFIG_DIR", "CODEX_SQLITE_HOME",
		"YAMNAYA_PROFILE", "YAMNAYA_ACCOUNT", "TMUX", "TMUX_PANE", "XPC_SERVICE_NAME"} {
		os.Unsetenv(env)
	}
	os.Setenv("YAMNAYA_BRIEFS_ADDR", "127.0.0.1:0")
	pinGo()
	for env, sub := range map[string]string{"HOME": "home", "TMPDIR": "tmp", "YAMNAYA_STATE_DIR": "home/.local/state/yamnaya", "CLAUDE_CONFIG_DIR": "claude", "CODEX_HOME": "codex"} {
		path := filepath.Join(root, sub)
		if err := os.MkdirAll(path, 0o755); err != nil {
			panic(err)
		}
		os.Setenv(env, path)
	}
	if err := os.WriteFile(filepath.Join(root, "home", ".gitconfig"), []byte("[user]\n\tname = ym test\n\temail = ymtest@example.invalid\n[init]\n\tdefaultBranch = main\n"), 0o644); err != nil {
		panic(err)
	}
	socks := []string{fmt.Sprintf("ymt-%d-ym", os.Getpid()), fmt.Sprintf("ymt-%d-tmux", os.Getpid())}
	os.Setenv("YAMNAYA_YM_SOCKET", socks[0])
	os.Setenv("YAMNAYA_TMUX_SOCKET", socks[1])
	defer func() {
		for _, s := range socks {
			exec.Command("tmux", "-L", s, "kill-server").Run()
		}
	}()
	f := &fence{root: root}
	proc.Signal = f.signal
	return f.check(m.Run())
}

const rootEnv = "YM_AGENTTEST_ROOT"

// pinGo keeps the go command a test runs on the owner's module and build caches and go env file, which would otherwise
// follow HOME into the root and download every module there.
func pinGo() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		gopath = filepath.Join(home, "go")
		os.Setenv("GOPATH", gopath)
	}
	if os.Getenv("GOMODCACHE") == "" {
		os.Setenv("GOMODCACHE", filepath.Join(filepath.SplitList(gopath)[0], "pkg", "mod"))
	}
	if cache, err := os.UserCacheDir(); err == nil && os.Getenv("GOCACHE") == "" {
		os.Setenv("GOCACHE", filepath.Join(cache, "go-build"))
	}
	if config, err := os.UserConfigDir(); err == nil && os.Getenv("GOENV") == "" {
		os.Setenv("GOENV", filepath.Join(config, "go", "env"))
	}
}

// fence lets a signal through only to a process the test started or one working inside its root.
type fence struct {
	root string
	mu   sync.Mutex
	bad  []string
}

func (f *fence) signal(pid int, sig syscall.Signal) error {
	if f.ours(pid) {
		return syscall.Kill(pid, sig)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bad = append(f.bad, fmt.Sprintf("%s to pid %d", sig, pid))
	return syscall.EPERM
}

func (f *fence) ours(pid int) bool {
	if pid <= 1 {
		return false
	}
	if cwd := proc.Cwd(pid); cwd == f.root || strings.HasPrefix(cwd, f.root+string(filepath.Separator)) {
		return true
	}
	procs, err := proc.List()
	if err != nil {
		return false
	}
	for _, p := range proc.Index(procs).Ancestors(pid) {
		if p.PID == os.Getpid() {
			return true
		}
	}
	return false
}

// check is code, or 1 when the run sent a refused signal.
func (f *fence) check(code int) int {
	if refused := f.refused(); len(refused) > 0 {
		fmt.Fprintln(os.Stderr, "agenttest: refused signals to processes outside the test:", strings.Join(refused, ", "))
		code = max(code, 1)
	}
	return code
}

func (f *fence) refused() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bad
}
