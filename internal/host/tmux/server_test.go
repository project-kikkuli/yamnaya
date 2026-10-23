package tmux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
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
		{"", ""},
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
	if hd, ok := h.Locate(p.TTY); !ok || hd.Host != Server || hd.ID != p.ID {
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
	if all, err := h.Panes(); err != nil || all["repo-wt-x"] != (Pane{}) {
		t.Error("Panes still lists the killed session")
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
