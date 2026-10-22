package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// remoteHosts reads the boards of the hosts ym remote reached over ssh, every host at once, and keeps the last read of each.
type remoteHosts struct {
	names []string
	ssh   string
	// timeout bounds one host's read; every is how old a read gets before the next Boards starts another; wait is how long
	// Boards waits for a host it has never read.
	timeout, every, wait time.Duration
	// seen hears each host that answered, with what it printed.
	seen func(host string, at time.Time, board []byte)

	mu   sync.Mutex
	last map[string]core.RemoteBoard
	// kept are boards an earlier process read, shown until this one reads the host.
	kept    map[string]core.RemoteBoard
	polled  map[string]time.Time
	running map[string]chan struct{}
}

func newRemoteHosts(names []string, wait time.Duration, seen func(string, time.Time, []byte)) *remoteHosts {
	return &remoteHosts{names: names, ssh: sshBin(), timeout: 8 * time.Second, every: 5 * time.Second, wait: wait, seen: seen,
		last: map[string]core.RemoteBoard{}, polled: map[string]time.Time{}, running: map[string]chan struct{}{}, kept: map[string]core.RemoteBoard{}}
}

// sshBin is the ssh ym runs: $YAMNAYA_SSH_BIN, else ssh.
func sshBin() string {
	if bin := os.Getenv("YAMNAYA_SSH_BIN"); bin != "" {
		return bin
	}
	return "ssh"
}

// keep seeds host's board with one an earlier process read, shown as stale until the host answers this one.
func (r *remoteHosts) keep(host string, at time.Time, raw []byte) {
	var j boardJSON
	if json.Unmarshal(raw, &j) != nil {
		return
	}
	b := remoteBoard(host, j)
	b.At, b.Stale = at, true
	r.mu.Lock()
	r.kept[host] = b
	r.mu.Unlock()
}

func (r *remoteHosts) Hosts() []string { return r.names }

func (r *remoteHosts) Boards() []core.RemoteBoard {
	r.mu.Lock()
	var first []chan struct{}
	for _, h := range r.names {
		_, read := r.last[h]
		ch, busy := r.running[h]
		if !busy && (!read || time.Since(r.polled[h]) >= r.every) {
			ch = make(chan struct{})
			r.running[h], r.polled[h] = ch, time.Now()
			go r.read(h, ch)
			busy = true
		}
		if busy && !read {
			first = append(first, ch)
		}
	}
	r.mu.Unlock()
	deadline := time.NewTimer(r.wait)
	defer deadline.Stop()
wait:
	for _, ch := range first {
		select {
		case <-ch:
		case <-deadline.C:
			break wait
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]core.RemoteBoard, 0, len(r.names))
	for _, h := range r.names {
		b, ok := r.last[h]
		if !ok {
			b, ok = r.kept[h]
			if !ok {
				b = core.RemoteBoard{Host: h}
			}
			if r.wait > 0 {
				b.Err = fmt.Errorf("no answer in %s", r.wait)
			}
		}
		out = append(out, b)
	}
	return out
}

func (r *remoteHosts) read(host string, done chan struct{}) {
	b := core.RemoteBoard{Host: host}
	out, err := r.board(host)
	var j boardJSON
	if err == nil {
		if err = json.Unmarshal(out, &j); err != nil {
			err = fmt.Errorf("ym board printed no board: %w", err)
		}
	}
	r.mu.Lock()
	if err != nil {
		if prev, ok := r.last[host]; ok && !prev.At.IsZero() {
			b = prev
		} else if prev, ok := r.kept[host]; ok {
			b = prev
		}
		b.Err = err
	} else {
		b = remoteBoard(host, j)
		b.At = time.Now()
	}
	r.mu.Unlock()
	if err == nil && r.seen != nil {
		r.seen(host, b.At, out)
	}
	r.mu.Lock()
	r.last[host] = b
	delete(r.running, host)
	r.mu.Unlock()
	close(done)
}

func (r *remoteHosts) board(host string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, r.ssh, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host, remoteCommand([]string{"board"}))
	c.WaitDelay = time.Second
	var errb bytes.Buffer
	c.Stderr = &errb
	out, err := c.Output()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("no answer in %s", r.timeout)
	}
	if err != nil {
		return nil, sshErr(err, errb.Bytes())
	}
	return out, nil
}

func (r *remoteHosts) Run(host string, args ...string) (string, error) {
	out, err := r.call(host, nil, args...)
	if err != nil {
		return "", fmt.Errorf("ym %s on %s: %w", strings.Join(args, " "), host, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// call runs ym args on host with stdin; an ssh that cannot reach the host fails with core.ErrUnreachable.
func (r *remoteHosts) call(host string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*r.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, r.ssh, "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", host, remoteCommand(args))
	c.WaitDelay = time.Second
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var errb bytes.Buffer
	c.Stderr = &errb
	out, err := c.Output()
	switch {
	case ctx.Err() != nil:
		return nil, fmt.Errorf("no answer in %s: %w", 2*r.timeout, core.ErrUnreachable)
	case err != nil:
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 255 {
			return nil, fmt.Errorf("%w: %w", core.ErrUnreachable, sshErr(err, errb.Bytes()))
		}
		return nil, sshErr(err, errb.Bytes())
	}
	return out, nil
}

// Sync carries one relay round trip to host through its ym relay sync.
func (r *remoteHosts) Sync(host string, req core.RelaySync) (core.RelayReply, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return core.RelayReply{}, err
	}
	out, err := r.call(host, raw, "relay", "sync")
	if err != nil {
		return core.RelayReply{}, err
	}
	var reply core.RelayReply
	if err := json.Unmarshal(out, &reply); err != nil {
		return reply, fmt.Errorf("ym relay sync on %s printed no reply: %w", host, err)
	}
	return reply, nil
}

func (r *remoteHosts) Attach(host string, args ...string) *exec.Cmd {
	c := exec.Command(r.ssh, "-t", host, remoteCommand(args))
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	return c
}

// sshErr is err with the last line ssh or the remote ym wrote to stderr.
func sshErr(err error, stderr []byte) error {
	lines := strings.Split(strings.TrimSpace(string(stderr)), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return fmt.Errorf("%w: %s", err, last)
	}
	return err
}

// readRemotes routes commands to every host ym remote reached, waiting up to wait for a host's first answer; show also puts their
// boards on this one.
func (e *env) readRemotes(wait time.Duration, show bool) error {
	hosts, err := e.core.Store.KnownHosts()
	if err != nil || len(hosts) == 0 {
		return err
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	st := e.core.Store
	r := newRemoteHosts(names, wait, func(h string, at time.Time, board []byte) {
		st.SeeHost(h, at)
		if show {
			st.KeepBoard(h, at, board)
		}
	})
	if show {
		for _, h := range names {
			if at, raw, ok, _ := st.KeptBoard(h); ok {
				r.keep(h, at, raw)
			}
		}
	}
	e.core.Remotes = r
	e.core.ShowRemotes = show
	return nil
}
