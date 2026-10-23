package tmux

import (
	"fmt"
	"maps"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// Server is the tmux server ym owns for the sessions it launches; it never touches any other server.
const Server = "ym"

// ServerConf is ym's own server config: panes stay after their agent exits so ym can tell and revive them.
const ServerConf = `set -g remain-on-exit on
set -g history-limit 50000
set -g escape-time 10
set -g focus-events on
set -g mouse on
set -g status-left "ym #S "
set -g status-left-length 60
set -g status-right "C-b d: back to the board "
`

// NewServer is the host for ym's own server, named by $YAMNAYA_YM_SOCKET or Server.
func NewServer() *Host {
	sock := os.Getenv("YAMNAYA_YM_SOCKET")
	if sock == "" {
		sock = Server
	}
	return &Host{Socket: sock, Self: os.Getenv("TMUX_PANE"), Kind: Server}
}

// SessionName is a tmux-safe session name for a launch key.
func SessionName(key string) string {
	return strings.NewReplacer(".", "-", ":", "-", " ", "-").Replace(key)
}

// agentEnv drops the variables a running agent sets for its own children, so agents ym starts never think they are nested.
// AgentEnv is env without what marks a process as running inside an agent's session: its tmux client and pane, and Claude
// Code's and Codex's session variables.
func AgentEnv(env []string) []string { return agentEnv(env) }

func agentEnv(env []string) []string {
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch {
		case k == "TMUX", k == "TMUX_PANE", k == "CLAUDECODE", k == "AI_AGENT", k == "CLAUDE_PID", k == "CLAUDE_EFFORT":
		case strings.HasPrefix(k, "CLAUDE_CODE_"):
		case strings.HasPrefix(k, "CODEX_") && k != "CODEX_HOME":
		default:
			out = append(out, kv)
		}
	}
	return out
}

func (h *Host) command(args ...string) *exec.Cmd {
	cmd := exec.Command("tmux", append([]string{"-u", "-L", h.socketName()}, args...)...)
	cmd.Env = agentEnv(os.Environ())
	return cmd
}

// Pane is the one pane of a launched session.
type Pane struct {
	Session string
	ID      string
	TTY     string
	Dead    bool
	Status  int
	Signal  string
	PID     int
}

const paneFormat = "#{pane_id}\t#{pane_tty}\t#{pane_dead}\t#{pane_dead_status}\t#{pane_pid}\t#{pane_dead_signal}"

// Start runs argv in a new detached session named name with cwd, starting the server from conf when it is down.
func (h *Host) Start(conf, name, cwd string, argv []string) (Pane, error) {
	args := []string{"-f", conf, "new-session", "-d", "-P", "-F", paneFormat, "-s", name, "-c", cwd, "-x", "200", "-y", "50"}
	cmd := h.command(append(args, argv...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return Pane{}, fmt.Errorf("tmux -L %s new-session %s: %w: %s", h.socketName(), name, err, strings.TrimSpace(string(out)))
	}
	h.forget()
	if p, err := parsePane(name, strings.TrimRight(string(out), "\n")); err == nil {
		return p, nil
	}
	// A loaded host can end the tmux client before it writes all of -P's line, though the session started.
	return h.Pane(name)
}

// Respawn restarts a session's pane with argv in cwd, killing whatever still runs there.
func (h *Host) Respawn(name, cwd string, argv []string) error {
	defer h.forget()
	out, err := h.command(append([]string{"respawn-pane", "-k", "-t", "=" + name + ":", "-c", cwd}, argv...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("tmux respawn-pane %s: %w: %s", name, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Capture is the visible text of session name's pane.
func (h *Host) Capture(name string) (string, error) {
	return h.run("capture-pane", "-p", "-t", "="+name+":")
}

// Pane reads the pane of session name; ErrNotFound when tmux says the session or its server is gone, any other error when tmux
// could not say.
func (h *Host) Pane(name string) (Pane, error) {
	out, err := h.run("list-panes", "-t", "="+name+":", "-F", paneFormat)
	if gone(err) {
		return Pane{}, fmt.Errorf("%s: %w", name, core.ErrNotFound)
	}
	if err != nil {
		return Pane{}, err
	}
	return parsePane(name, out)
}

// Panes is the first pane of every session on the server, by session name; none when the server is down, an error when tmux
// could not list them.
func (h *Host) Panes() (map[string]Pane, error) {
	_, sessions, err := h.table()
	return maps.Clone(sessions), err
}

func parsePane(name, out string) (Pane, error) {
	f := strings.Split(strings.Split(out, "\n")[0], "\t")
	if len(f) != 6 {
		return Pane{}, fmt.Errorf("tmux pane %s: unexpected %q", name, out)
	}
	status, _ := strconv.Atoi(f[3])
	pid, _ := strconv.Atoi(f[4])
	return Pane{Session: name, ID: f[0], TTY: f[1], Dead: f[2] == "1", Status: status, Signal: f[5], PID: pid}, nil
}

// Kill ends session name; a session that is already gone is not an error.
func (h *Host) Kill(name string) error {
	defer h.forget()
	out, err := h.command("kill-session", "-t", "="+name).CombinedOutput()
	if err == nil {
		return nil
	}
	text := strings.TrimSpace(string(out))
	if absent(text) {
		return nil
	}
	return fmt.Errorf("tmux kill-session %s: %w: %s", name, err, text)
}

// KillServer ends the server and every agent running in it; a server that is not running is not an error.
func (h *Host) KillServer() error {
	defer h.forget()
	out, err := h.command("kill-server").CombinedOutput()
	if text := strings.TrimSpace(string(out)); err != nil && !absent(text) {
		return fmt.Errorf("tmux kill-server: %w: %s", err, text)
	}
	return nil
}

// Attach is the command that shows the session holding hd in the caller's terminal until the owner detaches.
// Inside this server, Focus switches the client instead, so Attach is nil there.
func (h *Host) Attach(hd core.Handle) *exec.Cmd {
	if h.Kind != Server || h.inside() {
		return nil
	}
	name, err := h.run("display-message", "-p", "-t", hd.ID, "#{session_name}")
	if err != nil {
		return nil
	}
	cmd := h.command("attach-session", "-t", "="+name)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd
}

// Running is the pid of the program in pane id; ok is false once the pane is gone or its program exited.
func (h *Host) Running(id string) (pid int, ok bool) {
	out, err := h.run("display-message", "-p", "-t", id, "#{pane_dead} #{pane_pid}")
	dead, p, _ := strings.Cut(out, " ")
	if err != nil || dead != "0" {
		return 0, false
	}
	pid, err = strconv.Atoi(p)
	return pid, err == nil
}

// Keys presses keys in pane id.
func (h *Host) Keys(id string, keys ...string) error {
	_, err := h.run(append([]string{"send-keys", "-t", id}, keys...)...)
	return err
}
