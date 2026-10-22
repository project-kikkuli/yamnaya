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
	// seen hears each host that answered.
	seen func(host string, at time.Time)

	mu      sync.Mutex
	last    map[string]core.RemoteBoard
	polled  map[string]time.Time
	running map[string]chan struct{}
}

func newRemoteHosts(names []string, wait time.Duration, seen func(string, time.Time)) *remoteHosts {
	return &remoteHosts{names: names, ssh: "ssh", timeout: 8 * time.Second, every: 5 * time.Second, wait: wait, seen: seen,
		last: map[string]core.RemoteBoard{}, polled: map[string]time.Time{}, running: map[string]chan struct{}{}}
}

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
			b = core.RemoteBoard{Host: h}
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
	if err != nil {
		b.Err = err
	} else {
		b = remoteBoard(host, j)
		b.At = time.Now()
		if r.seen != nil {
			r.seen(host, b.At)
		}
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
	c := exec.Command(r.ssh, "-o", "BatchMode=yes", host, remoteCommand(args))
	out, err := c.Output()
	if err != nil {
		var stderr []byte
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = exit.Stderr
		}
		return "", fmt.Errorf("ym %s on %s: %w", strings.Join(args, " "), host, sshErr(err, stderr))
	}
	return strings.TrimSpace(string(out)), nil
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

// readRemotes makes the board read every host ym remote reached, waiting up to wait for a host's first answer.
func (e *env) readRemotes(wait time.Duration) error {
	hosts, err := e.core.Store.KnownHosts()
	if err != nil || len(hosts) == 0 {
		return err
	}
	names := make([]string, len(hosts))
	for i, h := range hosts {
		names[i] = h.Name
	}
	st := e.core.Store
	e.core.Remotes = newRemoteHosts(names, wait, func(h string, at time.Time) { st.SeeHost(h, at) })
	return nil
}
