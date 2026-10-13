package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	Role    string
}

const tagOption = "@ym_window"

// RoleOption marks the panes of the ym home window: the sidebar and the resting stage placeholder.
const RoleOption = "@ym_role"

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
// The pane is made to remain on exit before argv starts, so a harness that dies at once leaves its output behind.
// The tag tells a ym window apart from a later window that reuses its tmux id.
func (c Client) NewWindow(session, name, tag, cwd string, env map[string]string, argv []string) (string, error) {
	var a []string
	if c.HasSession(session) {
		a = []string{"new-window", "-d", "-t", "=" + session + ":", "-n", name}
	} else {
		a = []string{"new-session", "-d", "-s", session, "-n", name}
	}
	a = append(append(a, "-c", cwd, "-P", "-F", "#{window_id}"), envArgs(env)...)
	a = append(a, "sleep", "86400")
	id, err := c.run(a...)
	if err != nil {
		return "", err
	}
	if _, err := c.run("set-option", "-w", "-t", id, tagOption, tag, ";", "set-option", "-p", "-t", id, "remain-on-exit", "on"); err != nil {
		return "", err
	}
	if err := c.Respawn(id, cwd, env, argv); err != nil {
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
	out, err := c.run("list-windows", "-a", "-F", "#{window_id}\t#{session_name}\t#{window_name}\t#{"+tagOption+"}\t#{pane_dead}\t#{window_index}\t#{"+RoleOption+"}\t#{pane_current_command}")
	if err != nil {
		if strings.Contains(err.Error(), "no server running") || strings.Contains(err.Error(), "error connecting") {
			return map[string]WindowInfo{}, nil
		}
		return nil, err
	}
	windows := map[string]WindowInfo{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 8 {
			continue
		}
		idx, _ := strconv.Atoi(f[5])
		windows[f[0]] = WindowInfo{ID: f[0], Session: f[1], Name: f[2], Tag: f[3], Dead: f[4] == "1", Index: idx, Role: f[6], Command: f[7]}
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

type PanePlace struct {
	ID      string
	Window  string
	Session string
	Role    string
	Dead    bool
}

func parsePane(line string) PanePlace {
	f := strings.Split(line, "\t")
	for len(f) < 5 {
		f = append(f, "")
	}
	return PanePlace{ID: f[0], Window: f[1], Session: f[2], Dead: f[3] == "1", Role: f[4]}
}

const paneFormat = "#{pane_id}\t#{window_id}\t#{session_name}\t#{pane_dead}\t#{" + RoleOption + "}"

// Pane describes one pane, wherever it currently sits; display-message quietly falls back to another pane for a missing target, so the id is checked.
func (c Client) Pane(target string) (PanePlace, error) {
	out, err := c.run("display-message", "-p", "-t", target, paneFormat)
	if err != nil {
		return PanePlace{}, err
	}
	p := parsePane(out)
	if (strings.HasPrefix(target, "%") && p.ID != target) || (strings.HasPrefix(target, "@") && p.Window != target) {
		return PanePlace{}, fmt.Errorf("tmux: no pane %s", target)
	}
	return p, nil
}

func (c Client) WindowPanes(window string) ([]PanePlace, error) {
	out, err := c.run("list-panes", "-t", window, "-F", paneFormat)
	if err != nil {
		return nil, err
	}
	var panes []PanePlace
	for _, line := range strings.Split(out, "\n") {
		panes = append(panes, parsePane(line))
	}
	return panes, nil
}

// SwapPane exchanges two panes between their windows without changing which pane is active.
func (c Client) SwapPane(src, dst string) error {
	_, err := c.run("swap-pane", "-d", "-s", src, "-t", dst)
	return err
}

// BreakPane moves pane into a new window of session, named name when given, and returns the window id.
func (c Client) BreakPane(pane, session, name string) (string, error) {
	a := []string{"break-pane", "-d", "-s", pane, "-t", "=" + session + ":", "-P", "-F", "#{window_id}"}
	if name != "" {
		a = append(a, "-n", name)
	}
	return c.run(a...)
}

func (c Client) SelectPane(target string) error {
	_, err := c.run("select-pane", "-t", target)
	return err
}

// SetPaneRole marks pane with role; an empty role clears the mark and makes the pane remain when its process exits, like any ym window.
func (c Client) SetPaneRole(pane, role string) error {
	if role == "" {
		_, err := c.run("set-option", "-p", "-u", "-t", pane, RoleOption, ";", "set-option", "-p", "-t", pane, "remain-on-exit", "on")
		return err
	}
	_, err := c.run("set-option", "-p", "-t", pane, RoleOption, role)
	return err
}

// NewHome creates a detached session whose one window holds a sidebar pane of width cols and a stage pane right of it, in one tmux command so no observer sees half of it.
func (c Client) NewHome(session, window, cwd string, env map[string]string, sidebar, stage []string, sidebarRole, stageRole string, cols int) error {
	a := append([]string{"new-session", "-d", "-s", session, "-n", window, "-x", "160", "-y", "48", "-c", cwd}, envArgs(env)...)
	a = append(append(a, sidebar...), ";", "set-option", "-p", RoleOption, sidebarRole, ";", "split-window", "-h", "-c", cwd)
	a = append(append(append(a, envArgs(env)...), stage...), ";", "set-option", "-p", RoleOption, stageRole,
		";", "select-pane", "-L", ";", "resize-pane", "-x", strconv.Itoa(cols))
	_, err := c.run(a...)
	return err
}

// Split opens a pane beside target, to its left when left is set, leaving target active, and returns the new pane id.
func (c Client) Split(target string, left bool, cwd string, env map[string]string, argv []string) (string, error) {
	a := []string{"split-window", "-h", "-d", "-t", target, "-c", cwd, "-P", "-F", "#{pane_id}"}
	if left {
		a = append(a, "-b")
	}
	a = append(a, envArgs(env)...)
	return c.run(append(a, argv...)...)
}

func (c Client) ResizeWidth(pane string, cols int) error {
	_, err := c.run("resize-pane", "-t", pane, "-x", strconv.Itoa(cols))
	return err
}

// Hook sets a session hook.
func (c Client) Hook(session, hook, command string) error {
	_, err := c.run("set-hook", "-t", session, hook, command)
	return err
}

func (c Client) Detach() error {
	_, err := c.run("detach-client")
	return err
}

func (c Client) KillPane(pane string) error {
	_, err := c.run("kill-pane", "-t", pane)
	return err
}

// SocketPath is where tmux keeps the server socket named name.
func SocketPath(name string) string {
	dir := os.Getenv("TMUX_TMPDIR")
	if dir == "" {
		dir = "/tmp"
	}
	return filepath.Join(dir, fmt.Sprintf("tmux-%d", os.Getuid()), name)
}

// KillServer stops the server and removes its socket file.
func (c Client) KillServer() {
	c.run("kill-server")
	if c.Socket != "" {
		os.Remove(SocketPath(c.Socket))
	}
}

// LastLines returns up to n of the last non-blank lines target's pane printed, reading into its scrollback: tmux scrolls a dead pane's output up to show its exit status.
func (c Client) LastLines(target string, n int) []string {
	out, err := c.run("capture-pane", "-p", "-S", "-200", "-t", target)
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "Pane is dead") {
			lines = append(lines, l)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

func (c Client) KillWindow(target string) error {
	_, err := c.run("kill-window", "-t", target)
	return err
}
