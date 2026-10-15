package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

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
	h := &Host{Socket: fmt.Sprintf("ymtest-%d-%d", os.Getpid(), time.Now().UnixNano()), Kind: Server}
	t.Cleanup(func() { exec.Command("tmux", "-L", h.Socket, "kill-server").Run() })
	conf := filepath.Join(t.TempDir(), "tmux.conf")
	if err := os.WriteFile(conf, []byte(ServerConf), 0o644); err != nil {
		t.Fatal(err)
	}
	return h, conf
}

func TestServerKeepsExitedAgentsAndRespawnsThem(t *testing.T) {
	h, conf := ymServer(t)
	t.Setenv("TMUX", "/tmp/tmux-0/default,1,0")
	p, err := h.Start(conf, "repo-wt-x", t.TempDir(), []string{"sh", "-c", "exit 3"})
	if err != nil {
		t.Fatal(err)
	}
	if hd, ok := h.Locate(p.TTY); !ok || hd.Host != Server || hd.ID != p.ID {
		t.Errorf("Locate(%s) = %+v, %v", p.TTY, hd, ok)
	}
	waitFor(t, "the agent to exit", func() bool {
		p, err = h.Pane("repo-wt-x")
		return err == nil && p.Dead
	})
	if p.Status != 3 {
		t.Errorf("exit status %d, want 3", p.Status)
	}
	if err := h.Respawn("repo-wt-x", t.TempDir(), []string{"cat"}); err != nil {
		t.Fatal(err)
	}
	if p, err = h.Pane("repo-wt-x"); err != nil || p.Dead {
		t.Fatalf("after respawn %+v, %v", p, err)
	}
	if c := h.Attach(core.Handle{ID: p.ID}); c == nil || !slices.Contains(c.Args, "=repo-wt-x") || slices.ContainsFunc(c.Env, func(kv string) bool { return len(kv) > 5 && kv[:5] == "TMUX=" }) {
		t.Errorf("Attach outside the server = %v", c)
	}
	sock, _ := h.run("display-message", "-p", "#{socket_path}")
	t.Setenv("TMUX", sock+",1,0")
	if c := h.Attach(core.Handle{ID: p.ID}); c != nil {
		t.Errorf("Attach inside the server = %v, want nil so Focus switches the client", c.Args)
	}
	if err := h.Kill("repo-wt-x"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Pane("repo-wt-x"); !errors.Is(err, core.ErrNotFound) {
		t.Errorf("Pane after Kill = %v, want ErrNotFound", err)
	}
	if err := h.Kill("repo-wt-x"); err != nil {
		t.Errorf("Kill of a gone session = %v", err)
	}
}

func TestDefaultHostNeverAttaches(t *testing.T) {
	if c := (&Host{}).Attach(core.Handle{ID: "%1"}); c != nil {
		t.Errorf("default host Attach = %v", c.Args)
	}
}
