// Package tmux finds, focuses and types into tmux panes. It changes no layout but the split it opens beside ym's own pane.
package tmux

import (
	"bytes"
	"errors"
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

	mu       sync.Mutex
	panes    map[string]pane
	sessions map[string]Pane
	at       time.Time
	// stamp is the latest terminal fingerprint, listed the one the table was read at, moved when stamp last changed.
	stamp, listed uint64
	stamped       bool
	moved         time.Time
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
	// -u keeps tmux from turning the tabs in -F output into underscores when this process has no UTF-8 locale, as under launchd.
	cmd := exec.Command("tmux", append([]string{"-u"}, args...)...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return "", &runError{args: args, err: err, stderr: strings.TrimSpace(errb.String())}
	}
	return strings.TrimRight(out.String(), "\n"), nil
}

type runError struct {
	args   []string
	err    error
	stderr string
}

func (e *runError) Error() string {
	return fmt.Sprintf("tmux %s: %v: %s", strings.Join(e.args, " "), e.err, e.stderr)
}

func (e *runError) Unwrap() error { return e.err }

// absent reports whether tmux's message says the session it was asked about, or the whole server, does not exist; a server ending
// after its last session went says it has no current target.
func absent(text string) bool {
	return strings.HasPrefix(text, "can't find session:") || strings.HasPrefix(text, "no server running on ") || text == "no current target" ||
		strings.HasPrefix(text, "error connecting to ") && strings.HasSuffix(text, "(No such file or directory)")
}

// gone reports whether err is tmux saying the session or server does not exist, rather than tmux failing to answer.
func gone(err error) bool {
	var re *runError
	return errors.As(err, &re) && absent(re.stderr)
}

// list caches the pane table for a moment so one board refresh runs tmux once.
func (h *Host) list() map[string]pane {
	panes, _, _ := h.table()
	return panes
}

// table reads every pane once: live panes by terminal, and the first pane of each session by session name. A server that is
// not running has no panes; any other failure to list them is an error and leaves nothing cached, never an empty table.
func (h *Host) table() (map[string]pane, map[string]Pane, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.panes != nil && (time.Since(h.at) < 500*time.Millisecond || h.quiet()) {
		return h.panes, h.sessions, nil
	}
	at, listed := time.Now(), h.stamp
	out, err := h.run("list-panes", "-a", "-F", "#{pane_tty}\t#{pane_id}\t#{session_name}:#{window_index}.#{pane_index}\t#{session_name}\t"+paneFormat)
	if err != nil && !gone(err) {
		h.panes, h.sessions = nil, nil
		return map[string]pane{}, map[string]Pane{}, err
	}
	h.panes, h.sessions, h.at, h.listed = map[string]pane{}, map[string]Pane{}, at, listed
	if err != nil {
		return h.panes, h.sessions, nil
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.SplitN(line, "\t", 5)
		if len(f) != 5 {
			continue
		}
		if p, err := parsePane(f[3], f[4]); err == nil {
			if _, seen := h.sessions[f[3]]; !seen {
				h.sessions[f[3]] = p
			}
			if _, seen := h.panes[f[0]]; !p.Dead && !seen {
				h.panes[f[0]] = pane{id: f[1], target: f[2], session: f[3]}
			}
		}
	}
	return h.panes, h.sessions, nil
}

// settle is how long after a terminal process changes the pane table is still reread, while tmux catches up with it.
const settle = 2 * time.Second

// recheck bounds how long a table read while terminals stayed the same is kept, for renames and moves that change no process.
const recheck = 10 * time.Second

// quiet reports whether no terminal process changed since the table was read; h.mu is held.
func (h *Host) quiet() bool {
	return h.stamped && h.listed == h.stamp && time.Since(h.moved) >= settle && time.Since(h.at) < recheck
}

// Terminals records the fingerprint of the processes on terminals; the pane table is reread only after it changes.
func (h *Host) Terminals(stamp uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.stamped || stamp != h.stamp {
		h.stamp, h.stamped, h.moved = stamp, true, time.Now()
	}
}

// forget drops the cached pane table after ym changes the server's panes.
func (h *Host) forget() {
	h.mu.Lock()
	h.panes, h.sessions = nil, nil
	h.mu.Unlock()
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
