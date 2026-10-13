// Package tmux finds, focuses and types into tmux panes. It never changes layout.
package tmux

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
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

	mu    sync.Mutex
	panes map[string]pane
	at    time.Time
}

func New() *Host {
	return &Host{Socket: os.Getenv("YAMNAYA_TMUX_SOCKET"), Self: os.Getenv("TMUX_PANE")}
}

func (h *Host) Name() string { return Name }

type pane struct {
	id, target, session string
}

func (h *Host) run(args ...string) (string, error) {
	if h.Socket != "" {
		args = append([]string{"-L", h.Socket}, args...)
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
	out, err := h.run("list-panes", "-a", "-F", "#{pane_tty}\t#{pane_id}\t#{session_name}:#{window_index}.#{pane_index}\t#{session_name}")
	if err != nil {
		return h.panes
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) == 4 {
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
	return core.Handle{Host: Name, ID: p.id, Label: p.target}, true
}

type client struct {
	tty, session string
	activity     int64
}

func (h *Host) clients() []client {
	out, err := h.run("list-clients", "-F", "#{client_tty}\t#{client_session}\t#{client_activity}")
	if err != nil {
		return nil
	}
	var cs []client
	for _, line := range strings.Split(out, "\n") {
		f := strings.Split(line, "\t")
		if len(f) != 3 {
			continue
		}
		act, _ := strconv.ParseInt(f[2], 10, 64)
		cs = append(cs, client{tty: f[0], session: f[1], activity: act})
	}
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].activity > cs[j].activity })
	return cs
}

// Focus selects the pane's window and pane and points a client at its session.
// Inside tmux it moves ym's own client; outside, the most recently active client, whose terminal it returns.
func (h *Host) Focus(hd core.Handle) (string, error) {
	sel := []string{"select-window", "-t", hd.ID, ";", "select-pane", "-t", hd.ID}
	if h.Self != "" && os.Getenv("TMUX") != "" {
		_, err := h.run(append([]string{"switch-client", "-t", hd.ID, ";"}, sel...)...)
		return "", err
	}
	cs := h.clients()
	if len(cs) == 0 {
		return "", fmt.Errorf("no tmux client is attached to show %s", hd.Label)
	}
	if _, err := h.run(append([]string{"switch-client", "-c", cs[0].tty, "-t", hd.ID, ";"}, sel...)...); err != nil {
		return "", err
	}
	return cs[0].tty, nil
}

// Type sends text literally, then Enter as a separate key so the harness does not read it as a pasted newline.
func (h *Host) Type(hd core.Handle, text string) error {
	if _, err := h.run("send-keys", "-t", hd.ID, "-l", text); err != nil {
		return err
	}
	time.Sleep(150 * time.Millisecond)
	_, err := h.run("send-keys", "-t", hd.ID, "Enter")
	return err
}

func (h *Host) Screen(hd core.Handle) (string, error) {
	return h.run("capture-pane", "-p", "-t", hd.ID)
}
