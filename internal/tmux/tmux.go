package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Client struct {
	Socket string
}

type WindowInfo struct {
	ID      string
	Session string
	Name    string
	Tag     string
	Dead    bool
	Index   int
	Command string
}

const tagOption = "@ym_window"

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

// NewWindow starts argv in a new window tagged with tag, creating the session when needed, and returns the window id.
// The tag tells a ym window apart from a later window that reuses its tmux id.
func (c Client) NewWindow(session, name, tag, cwd string, env map[string]string, argv []string) (string, error) {
	var a []string
	if c.HasSession(session) {
		a = []string{"new-window", "-d", "-t", "=" + session + ":", "-n", name}
	} else {
		a = []string{"new-session", "-d", "-s", session, "-n", name}
	}
	a = append(a, "-c", cwd, "-P", "-F", "#{window_id}")
	a = append(a, envArgs(env)...)
	a = append(a, argv...)
	id, err := c.run(a...)
	if err != nil {
		return "", err
	}
	if _, err := c.run("set-option", "-w", "-t", id, tagOption, tag); err != nil {
		return "", err
	}
	return id, nil
}

// Respawn replaces the process in target's pane with argv, keeping the window id.
func (c Client) Respawn(target, cwd string, env map[string]string, argv []string) error {
	a := append([]string{"respawn-pane", "-k", "-t", target, "-c", cwd}, envArgs(env)...)
	_, err := c.run(append(a, argv...)...)
	return err
}

// LastActivity is when target's window last produced output.
func (c Client) LastActivity(target string) (time.Time, error) {
	out, err := c.run("display-message", "-p", "-t", target, "#{window_activity}")
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(out, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("window_activity %q: %w", out, err)
	}
	return time.Unix(sec, 0), nil
}

func (c Client) KillSession(session string) error {
	_, err := c.run("kill-session", "-t", "="+session)
	return err
}

func (c Client) Windows() (map[string]WindowInfo, error) {
	out, err := c.run("list-windows", "-a", "-F", "#{window_id}\t#{session_name}\t#{window_name}\t#{"+tagOption+"}\t#{pane_dead}\t#{window_index}\t#{pane_current_command}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return map[string]WindowInfo{}, nil
		}
		return nil, err
	}
	windows := map[string]WindowInfo{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			continue
		}
		idx, _ := strconv.Atoi(f[5])
		windows[f[0]] = WindowInfo{ID: f[0], Session: f[1], Name: f[2], Tag: f[3], Dead: f[4] == "1", Index: idx, Command: f[6]}
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

func (c Client) Tag(target, tag string) error {
	_, err := c.run("set-option", "-w", "-t", target, tagOption, tag)
	return err
}

// WindowID resolves any tmux target to its window id.
func (c Client) WindowID(target string) (string, error) {
	return c.run("display-message", "-p", "-t", target, "#{window_id}")
}

// CurrentWindow is the window of the current or most recently active client.
func (c Client) CurrentWindow() (string, error) {
	return c.run("display-message", "-p", "#{window_id}")
}

// TypeText types text into target's active pane without pressing Enter.
func (c Client) TypeText(target, text string) error {
	_, err := c.run("send-keys", "-t", target, "-l", text)
	return err
}

// CapturePane returns the visible text of target's active pane.
func (c Client) CapturePane(target string) (string, error) {
	return c.run("capture-pane", "-p", "-t", target)
}
