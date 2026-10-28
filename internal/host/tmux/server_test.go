package tmux

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/host/tmux/tmuxtest"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

func TestPaneExitSignalsHaveTheSameNamesAcrossTmuxPlatforms(t *testing.T) {
	for _, tc := range []struct {
		signal string
		want   string
	}{
		{"kill", "kill"},
		{"term", "term"},
		{fmt.Sprint(int(syscall.SIGKILL)), "kill"},
		{fmt.Sprint(int(syscall.SIGTERM)), "term"},
		{fmt.Sprint(int(syscall.SIGINT)), "int"},
	} {
		t.Run(tc.signal, func(t *testing.T) {
			p, err := parsePane("task", "%1\t/dev/tty1\t1\t\t123\t"+tc.signal)
			if err != nil || !p.Dead || p.Signal != tc.want {
				t.Fatalf("pane = %+v, %v; want dead with signal %q", p, err, tc.want)
			}
		})
	}
}

func TestAPaneIsDeadOnlyOnceTmuxKnowsHowItsProcessEnded(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		dead       bool
		status     int
	}{
		{"running", "%1\t/dev/tty1\t0\t\t123\t", false, 0},
		{"terminal closed, process not yet reaped", "%1\t/dev/tty1\t1\t\t123\t", false, 0},
		{"exited 3", "%1\t/dev/tty1\t1\t3\t123\t", true, 3},
		{"exited 0", "%1\t/dev/tty1\t1\t0\t123\t", true, 0},
		{"killed", "%1\t/dev/tty1\t1\t\t123\tkill", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parsePane("task", tc.line)
			if err != nil || p.Dead != tc.dead || p.Status != tc.status {
				t.Fatalf("pane = %+v, %v; want dead %v with status %d", p, err, tc.dead, tc.status)
			}
		})
	}
}

func TestSocketOf(t *testing.T) {
	for _, c := range []struct{ env, want string }{
		{"/private/tmp/tmux-501/default,1912,0", "default"},
		{"/private/tmp/tmux-501/ym,77,3", "ym"},
		{"", ""},
	} {
		if got := socketOf(c.env); got != c.want {
			t.Errorf("socketOf(%q) = %q, want %q", c.env, got, c.want)
		}
	}
}

func TestSessionName(t *testing.T) {
	for in, want := range map[string]string{"repo-wt-x": "repo-wt-x", "my.repo-wt-a:b": "my-repo-wt-a-b"} {
		if got := SessionName(in); got != want {
			t.Errorf("SessionName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestAgentEnvDropsOnlyTheCallingAgentsOwnVariables(t *testing.T) {
	in := []string{"PATH=/bin", "TMUX=/tmp/x,1,0", "TMUX_PANE=%1", "CLAUDECODE=1", "CLAUDE_CODE_SESSION_ID=a", "CLAUDE_CONFIG_DIR=/c", "CODEX_HOME=/h", "CODEX_THREAD_ID=t", "AI_AGENT=x", "ANTHROPIC_BASE_URL=u"}
	want := []string{"PATH=/bin", "CLAUDE_CONFIG_DIR=/c", "CODEX_HOME=/h", "ANTHROPIC_BASE_URL=u"}
	if got := agentEnv(in); !slices.Equal(got, want) {
		t.Errorf("agentEnv = %v, want %v", got, want)
	}
}

func ymServer(t *testing.T) (*Host, string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	h := &Host{Socket: tmuxtest.Socket(t), Kind: Server}
	conf := filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(conf, []byte(ServerConf), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, conf
}

func TestServerKeepsExitedAgentsAndRespawnsThem(t *testing.T) {
	h, conf := ymServer(t)
	t.Setenv("TMUX", "/tmp/tmux-0/default,1,0")
	p, err := h.Start(conf, "repo-wt-x", t.TempDir(), []string{"sh", "-c", "read _; exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if hd, ok, _ := h.Locate(p.TTY); !ok || hd.Host != Server || hd.ID != p.ID {
		t.Errorf("Locate(%s) = %+v, %v", p.TTY, hd, ok)
	}
	if _, err := h.run("send-keys", "-t", p.ID, "Enter"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the board's pane table to show the agent exited", func() bool {
		all, err := h.Panes()
		return err == nil && all["repo-wt-x"].Dead
	})
	if p, err = h.Pane("repo-wt-x"); err != nil || !p.Dead || p.Status != 3 {
		t.Errorf("exited pane %+v, %v, want dead with status 3", p, err)
	}
	if all, err := h.Panes(); err != nil || all["repo-wt-x"] != p {
		t.Errorf("Panes()[repo-wt-x] = %+v, want the exited pane %+v", all["repo-wt-x"], p)
	}
	if err := h.Respawn("repo-wt-x", t.TempDir(), []string{"cat"}); err != nil {
		t.Fatal(err)
	}
	if p, err = h.Pane("repo-wt-x"); err != nil || p.Dead {
		t.Fatalf("after respawn %+v, %v", p, err)
	}
	if all, err := h.Panes(); err != nil || all["repo-wt-x"] != p {
		t.Errorf("Panes()[repo-wt-x] = %+v, want the respawned pane %+v", all["repo-wt-x"], p)
	}
	if c, _ := h.Attach(core.Handle{ID: p.ID}); c == nil || !slices.Contains(c.Args, p.ID) || slices.ContainsFunc(c.Env, func(kv string) bool { return len(kv) > 5 && kv[:5] == "TMUX=" }) {
		t.Errorf("Attach outside the server = %v", c)
	}
	sock, _ := h.run("display-message", "-p", "#{socket_path}")
	t.Setenv("TMUX", sock+",1,0")
	if c, _ := h.Attach(core.Handle{ID: p.ID}); c != nil {
		t.Errorf("Attach inside the server = %v, want nil so Focus switches the client", c.Args)
	}
	if err := h.Kill("repo-wt-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Pane("repo-wt-x"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Pane after Kill = %v, want ErrNotFound", err)
	}
	if all, err := h.Panes(); err != nil || all["repo-wt-x"] != (Pane{}) {
		t.Error("Panes still lists the killed session")
	}
	if err := h.Kill("repo-wt-x"); err != nil {
		t.Errorf("Kill of a gone session = %v", err)
	}
}

func TestDefaultHostNeverAttaches(t *testing.T) {
	if c, _ := (&Host{}).Attach(core.Handle{ID: "%1"}); c != nil {
		t.Errorf("default host Attach = %v", c.Args)
	}
}

func TestPanesReadTheSameWithOrWithoutAUTF8Locale(t *testing.T) {
	for _, tc := range []struct{ name, lcAll string }{
		{name: "UTF-8 locale", lcAll: "en_US.UTF-8"},
		{name: "no UTF-8 locale, as launchd starts ym serve", lcAll: "C"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := server(t)
			h.must(t, "new-session", "-d", "-s", "work", "cat")
			t.Setenv("LC_ALL", tc.lcAll)
			p, err := h.Pane("work")
			if err != nil || !strings.HasPrefix(p.ID, "%") || p.PID == 0 {
				t.Fatalf("Pane = %+v, %v", p, err)
			}
			h.forget()
			if panes, err := h.Panes(); err != nil || panes["work"].ID != p.ID {
				t.Fatalf("Panes = %+v, %v; want work as %s", panes, err, p.ID)
			}
		})
	}
}

func TestReloadBringsARunningServerOntoTheConfigAndLeavesADownOneDown(t *testing.T) {
	h, conf := ymServer(t)
	if err := h.Reload(conf); err != nil {
		t.Fatalf("Reload with no server = %v", err)
	}
	bare := filepath.Join(t.TempDir(), "bare.conf")
	os.WriteFile(bare, nil, 0o644)
	if _, err := h.Start(bare, "repo-wt-x", t.TempDir(), []string{"sh", "-c", "sleep 30"}); err != nil {
		t.Fatal(err)
	}
	if err := h.Reload(conf); err != nil {
		t.Fatal(err)
	}
	for opt, want := range map[string]string{"set-clipboard": "on", "allow-passthrough": "on"} {
		if got, _ := h.run("show-options", "-gqv", opt); got != want {
			if got, _ = h.run("show-options", "-sqv", opt); got != want {
				t.Errorf("%s = %q after Reload, want %q", opt, got, want)
			}
		}
	}
}

func TestServerPassesCopiesAndLinksToEveryClientTerminal(t *testing.T) {
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script not installed")
	}
	for _, term := range []string{"xterm-256color", "screen-256color", "tmux-256color"} {
		t.Run(term, func(t *testing.T) {
			h, conf := ymServer(t)
			emit := `read x; printf '\033[H\033[2J\033]52;c;aGk=\a\033]8;;https://e.x/a?b=1&c=2\033\\LINK\033]8;;\033\\'; read y`
			if _, err := h.Start(conf, "s", t.TempDir(), []string{"sh", "-c", emit}); err != nil {
				t.Fatal(err)
			}
			attach := []string{"tmux", "-L", h.Socket, "attach", "-t", "=s"}
			args := append([]string{"-q", "/dev/null"}, attach...)
			if runtime.GOOS == "linux" {
				args = []string{"-qfec", strings.Join(attach, " "), "/dev/null"}
			}
			var out lockedBuffer
			c := exec.Command(script, args...)
			c.Env = append(agentEnv(os.Environ()), "TERM="+term)
			c.Stdout, c.Stderr = &out, &out
			in, err := c.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := c.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { in.Close(); c.Process.Kill(); c.Wait() })
			waitFor(t, "the client to attach", func() bool { n, _ := h.run("list-clients"); return n != "" })
			if _, err := h.run("send-keys", "-t", "=s:", "Enter"); err != nil {
				t.Fatal(err)
			}
			waitFor(t, "the copy and the link to reach the client's terminal", func() bool {
				s := out.String()
				return strings.Contains(s, "\x1b]52;c;aGk=") && strings.Contains(s, ";https://e.x/a?b=1&c=2\x1b\\")
			})
		})
	}
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
