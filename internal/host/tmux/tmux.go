// Package tmux finds, focuses and types into tmux panes. It changes no layout but the split it opens beside ym's own pane.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

const Name = "tmux"

type Host struct {
	// Socket selects a tmux server by -L name; "" is the default server.
	Socket string
	// Self is the tmux pane ym itself runs in, from $TMUX_PANE.
	Self string
	// Kind names the host on handles; "" is Name.
	Kind string

	mu    sync.Mutex
	panes map[string]pane
	at    time.Time
}

func New() *Host {
	return &Host{Socket: os.Getenv("YAMNAYA_TMUX_SOCKET"), Self: os.Getenv("TMUX_PANE")}
}

func (h *Host) Name() string {
	if h.Kind != "" {
		return h.Kind
	}
	return Name
}

func (h *Host) socketName() string {
	if h.Socket != "" {
		return h.Socket
	}
	return "default"
}

// socketOf is the server name in a $TMUX value, the base of its socket path.
func socketOf(tmuxEnv string) string {
	path, _, _ := strings.Cut(tmuxEnv, ",")
	if path == "" {
		return ""
	}
	return filepath.Base(path)
}

// inside reports whether ym itself runs in this host's server.
func (h *Host) inside() bool { return socketOf(os.Getenv("TMUX")) == h.socketName() }

type pane struct {
	id, target, session string
}

func (h *Host) run(args ...string) (string, error) {
	if h.Socket != "" || (os.Getenv("TMUX") != "" && !h.inside()) {
		args = append([]string{"-L", h.socketName()}, args...)
	}
	cmd := exec.Command("tmux", args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("tmux %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

// list caches the pane table for a moment so one board refresh runs tmux once.
func (h *Host) list() map[string]pane {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.panes != nil && time.Since(h.at) < 500*time.Millisecond {
		return h.panes
	}
	h.panes, h.at = map[string]pane{}, time.Now()
	out, err := h.run("list-panes", "-a", "-F", "#{pane_tty}\t#{pane_id}\t#{session_name}:#{window_index}.#{pane_index}\t#{session_name}\t#{pane_dead}")
	if err != nil {
		return h.panes
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if _, seen := h.panes[f[0]]; len(f) == 5 && f[4] != "1" && !seen {
			h.panes[f[0]] = pane{id: f[1], target: f[2], session: f[3]}
		}
	}
	return h.panes
}

func (h *Host) Locate(tty string) (core.Handle, bool) {
	p, ok := h.list()[tty]
	if !ok {
		return core.Handle{}, false
	}
	return core.Handle{Host: h.Name(), ID: p.id, Label: p.target}, true
}

// Focus jumps ym's own tmux client to the pane. Outside this server it does nothing, so no other client or window ever moves.
func (h *Host) Focus(hd core.Handle) (string, error) {
	if h.Self == "" || !h.inside() {
		return "", core.ErrNoHost
	}
	_, err := h.run("switch-client", "-t", hd.ID, ";", "select-window", "-t", hd.ID, ";", "select-pane", "-t", hd.ID)
	return "", err
}

// Split shows hd in a new pane beside ym's own through a grouped session of hd's, so no other client's window moves.
// The split closes when its client detaches, even in a server that keeps exited panes.
func (h *Host) Split(hd core.Handle) error {
	if h.Self == "" || os.Getenv("TMUX") == "" {
		return core.ErrNoHost
	}
	session, err := h.run("display-message", "-p", "-t", hd.ID, "#{session_name}")
	if err != nil {
		return err
	}
	args := []string{"split-window", "-h", "-t", h.Self, "-e", "TMUX=", "tmux", "-L", h.socketName(),
		"new-session", "-t", "=" + session, `\;`, "set-option", "destroy-unattached", "on",
		`\;`, "select-window", "-t", hd.ID, `\;`, "select-pane", "-t", hd.ID,
		";", "set-option", "-p", "remain-on-exit", "off"}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("tmux split-window: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Paste pastes text as one bracketed paste, so neither harness reads fast input as a paste burst or a typed Enter.
func (h *Host) Paste(hd core.Handle, text string) error {
	buf := fmt.Sprintf("ym-%d-%d", os.Getpid(), time.Now().UnixNano())
	_, err := h.run("set-buffer", "-b", buf, "--", text, ";", "paste-buffer", "-p", "-d", "-b", buf, "-t", hd.ID)
	return err
}

func (h *Host) Enter(hd core.Handle) error {
	_, err := h.run("send-keys", "-t", hd.ID, "Enter")
	return err
}

func (h *Host) Screen(hd core.Handle) (string, error) {
	return h.run("capture-pane", "-p", "-t", hd.ID)
}
