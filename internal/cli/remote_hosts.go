package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
)

// remoteHosts reads the boards of the hosts ym remote reached over ssh, every host at once, and keeps the last read of each.
type remoteHosts struct {
	names []string
	// known re-reads the remembered hosts, so a host added or forgotten while a board runs shows or leaves; nil keeps names.
	known    func() []string
	rereadAt time.Time
	ssh      string
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
	// silent is why each host that stopped answering did not, until it answers again.
	silent map[string]error
}

func newRemoteHosts(names []string, wait time.Duration, seen func(string, time.Time, []byte)) *remoteHosts {
	return &remoteHosts{names: names, ssh: sshBin(), timeout: 8 * time.Second, every: 5 * time.Second, wait: wait, seen: seen,
		last: map[string]core.RemoteBoard{}, polled: map[string]time.Time{}, running: map[string]chan struct{}{}, kept: map[string]core.RemoteBoard{}, silent: map[string]error{}}
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

func (r *remoteHosts) Hosts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reread()
	return slices.Clone(r.names)
}

// reread takes the remembered hosts as they stand now, at most every two seconds; r.mu is held.
func (r *remoteHosts) reread() {
	if r.known != nil && time.Since(r.rereadAt) >= 2*time.Second {
		r.names, r.rereadAt = r.known(), time.Now()
	}
}

func (r *remoteHosts) Silent(host string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.silent[host] == nil {
		return nil
	}
	return fmt.Errorf("%w: %s", core.ErrUnreachable, r.last[host].State())
}

func (r *remoteHosts) Boards() []core.RemoteBoard {
	r.mu.Lock()
	r.reread()
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
	unreachable := errors.Is(err, core.ErrUnreachable)
	if unreachable {
		err = errors.Unwrap(err)
	}
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
		if b.FailingSince.IsZero() {
			b.FailingSince = time.Now()
		}
	} else {
		b = remoteBoard(host, j)
		if b.At = parseTS(j.At); b.At.IsZero() {
			b.At = time.Now()
		}
	}
	r.mu.Unlock()
	if err == nil && r.seen != nil {
		r.seen(host, b.At, out)
	}
	r.mu.Lock()
	r.last[host] = b
	r.silent[host] = nil
	if unreachable {
		r.silent[host] = err
	}
	delete(r.running, host)
	r.mu.Unlock()
	close(done)
}

func (r *remoteHosts) board(host string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, r.ssh, append(sshArgs(host, "-o", "BatchMode=yes"), remoteCommand([]string{"board"}))...)
	c.WaitDelay = time.Second
	var errb bytes.Buffer
	c.Stderr = &errb
	out, err := c.Output()
	switch {
	case ctx.Err() != nil:
		r.drop(host)
		return nil, wrapped{core.ErrUnreachable, fmt.Errorf("no answer in %s", r.timeout)}
	case err != nil:
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 255 {
			r.drop(host)
			return nil, wrapped{core.ErrUnreachable, sshErr(err, errb.Bytes())}
		}
		return nil, sshErr(err, errb.Bytes())
	}
	return out, nil
}

// wrapped is err that also matches mark, reading as err alone.
type wrapped struct{ mark, err error }

func (w wrapped) Error() string   { return w.err.Error() }
func (w wrapped) Unwrap() error   { return w.err }
func (w wrapped) Is(t error) bool { return t == w.mark }

// drop ends the connection ym shares to host, so the next ssh there connects afresh instead of waiting on one that went silent.
func (r *remoteHosts) drop(host string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	exec.CommandContext(ctx, r.ssh, sshArgs(host, "-O", "exit")...).Run()
}

func (r *remoteHosts) Run(host string, args ...string) (string, error) {
	out, err := r.call(host, nil, args...)
	if err != nil {
		return "", fmt.Errorf("ym %s on %s: %w", strings.Join(args, " "), host, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Reach runs nothing on host, failing with core.ErrUnreachable when ssh cannot reach it within its connect timeout.
func (r *remoteHosts) Reach(host string) error {
	ctx, cancel := context.WithTimeout(context.Background(), r.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, r.ssh, append(sshArgs(host, "-o", "BatchMode=yes"), "true")...)
	c.WaitDelay = time.Second
	var errb bytes.Buffer
	c.Stderr = &errb
	err := c.Run()
	switch {
	case ctx.Err() != nil:
		r.drop(host)
		return fmt.Errorf("%s: no answer in %s: %w", host, r.timeout, core.ErrUnreachable)
	case err != nil:
		r.drop(host)
		return fmt.Errorf("%s: %w: %w", host, core.ErrUnreachable, sshErr(err, errb.Bytes()))
	}
	return nil
}

// call runs ym args on host with stdin; an ssh that cannot reach the host fails with core.ErrUnreachable.
func (r *remoteHosts) call(host string, stdin []byte, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*r.timeout)
	defer cancel()
	c := exec.CommandContext(ctx, r.ssh, append(sshArgs(host, "-o", "BatchMode=yes"), remoteCommand(args))...)
	c.WaitDelay = time.Second
	if stdin != nil {
		c.Stdin = bytes.NewReader(stdin)
	}
	var errb bytes.Buffer
	c.Stderr = &errb
	out, err := c.Output()
	switch {
	case ctx.Err() != nil:
		r.drop(host)
		return nil, fmt.Errorf("no answer in %s: %w", 2*r.timeout, core.ErrUnreachable)
	case err != nil:
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 255 {
			r.drop(host)
			return nil, fmt.Errorf("%w: %w", core.ErrUnreachable, sshErr(err, errb.Bytes()))
		}
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == exitGone {
			return nil, fmt.Errorf("%w: %w", core.ErrGone, sshErr(err, errb.Bytes()))
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

// Attach runs args on host in this terminal. What the session copies there lands on this Mac's pasteboard, a link right-clicked
// there opens here, and a link to a port on the host's own localhost works here: ym forwards that port over the connection.
func (r *remoteHosts) Attach(host string, args ...string) *exec.Cmd {
	c := exec.Command(r.ssh, append(sshArgs(host, "-t"), attachCommand(args))...)
	relay := &termRelay{out: os.Stdout, forward: func(port string) {
		if "127.0.0.1:"+port == core.BriefsAddr() {
			return
		}
		spec := "127.0.0.1:" + port + ":localhost:" + port
		go exec.Command(r.ssh, sshArgs(host, "-O", "forward", "-L", spec)...).Run()
	}}
	if runtime.GOOS == "darwin" {
		relay.copy = func(text []byte) {
			pb := exec.Command("/usr/bin/pbcopy")
			pb.Stdin = bytes.NewReader(hostBriefLinks(text, host))
			go pb.Run()
		}
		relay.open = func(url []byte) {
			go exec.Command("/usr/bin/open", string(hostBriefLinks(url, host))).Run()
		}
	}
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, relay, os.Stderr
	return c
}

var remoteBriefLink = regexp.MustCompile(`(https?://(?:127\.0\.0\.1|localhost):7477/b/)([^/\s?#]+(?:[?#][^\s]*)?)(\s|$)`)

// hostBriefLinks points the links in text to briefs on host's own serve at this machine's serve, which shows them at
// /b/<host>/<id>, since the same port here is this machine's.
func hostBriefLinks(text []byte, host string) []byte {
	return remoteBriefLink.ReplaceAll(text, []byte("${1}"+host+"/${2}${3}"))
}

// sshArgs are ssh's arguments up to the remote command: opts, then one connection to host shared by every ssh ym runs
// there and kept ten minutes after the last, so a read, relay or attach skips the handshake. A handshake that answers
// takes under half a second, so three seconds bound one that does not; keepalives end a shared connection within six
// seconds of the network going silent, so a call muxed onto it fails instead of hanging.
func sshArgs(host string, opts ...string) []string {
	return append(append(opts, "-o", "ConnectTimeout=3", "-o", "ControlMaster=auto", "-o", "ControlPath="+filepath.Join("/tmp", fmt.Sprintf("ym-%d-%%C", os.Getuid())),
		"-o", "ControlPersist=10m", "-o", "ServerAliveInterval=2", "-o", "ServerAliveCountMax=2"), host)
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
	st := e.core.Store
	known := func() []string {
		hosts, _ := st.KnownHosts()
		names := make([]string, len(hosts))
		for i, h := range hosts {
			names[i] = h.Name
		}
		return names
	}
	names := known()
	if len(names) == 0 && wait > 0 {
		return nil
	}
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
	if wait == 0 {
		r.known = known
	}
	e.core.Remotes = r
	e.core.ShowRemotes = show
	return nil
}
