package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
)

type Client struct {
	Socket string
}

type WindowInfo struct {
	ID      string
	Session string
	Name    string
	Dead    bool
}

func FromEnv() Client {
	return Client{Socket: os.Getenv("YAMNAYA_TMUX_SOCKET")}
}

func SessionName(project string) string { return "ym-" + project }

func (c Client) args(a ...string) []string {
	if c.Socket == "" {
		return a
	}
	return append([]string{"-L", c.Socket}, a...)
}

func (c Client) run(a ...string) (string, error) {
	cmd := exec.Command("tmux", c.args(a...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(a, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func envArgs(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	for _, k := range keys {
		out = append(out, "-e", k+"="+env[k])
	}
	return out
}

func (c Client) HasSession(session string) bool {
	_, err := c.run("has-session", "-t", "="+session)
	return err == nil
}

// NewWindow starts argv in a new window, creating the session when it does not exist, and returns the window id.
func (c Client) NewWindow(session, name, cwd string, env map[string]string, argv []string) (string, error) {
	var a []string
	if c.HasSession(session) {
		a = []string{"new-window", "-d", "-t", "=" + session + ":", "-n", name}
	} else {
		a = []string{"new-session", "-d", "-s", session, "-n", name}
	}
	a = append(a, "-c", cwd, "-P", "-F", "#{window_id}")
	a = append(a, envArgs(env)...)
	a = append(a, argv...)
	return c.run(a...)
}

func (c Client) KillSession(session string) error {
	_, err := c.run("kill-session", "-t", "="+session)
	return err
}

func (c Client) Windows() (map[string]WindowInfo, error) {
	out, err := c.run("list-windows", "-a", "-F", "#{window_id}\t#{session_name}\t#{window_name}\t#{pane_dead}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return map[string]WindowInfo{}, nil
		}
		return nil, err
	}
	windows := map[string]WindowInfo{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 4 {
			continue
		}
		windows[f[0]] = WindowInfo{ID: f[0], Session: f[1], Name: f[2], Dead: f[3] == "1"}
	}
	return windows, nil
}

// Jump switches the current client to target, or attaches when not inside tmux.
func (c Client) Jump(target string) error {
	if os.Getenv("TMUX") != "" {
		_, err := c.run("switch-client", "-t", target)
		return err
	}
	cmd := exec.Command("tmux", c.args("attach-session", "-t", target)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}
