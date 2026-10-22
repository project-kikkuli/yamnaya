package core

import (
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Remotes reports what other hosts' ym boards show and carries ym commands to those hosts.
type Remotes interface {
	// Boards is the last board each remembered host reported, in host order; it never waits on a host longer than the remote's own wait.
	Boards() []RemoteBoard
	// Run runs ym args on host and returns what it printed.
	Run(host string, args ...string) (string, error)
	// Attach is the command that runs ym args on host in the caller's terminal.
	Attach(host string, args ...string) *exec.Cmd
}

// RemoteBoard is one host's board as ym last read it over ssh.
type RemoteBoard struct {
	Host string
	// At is when the host last answered; zero while the first read is still running.
	At       time.Time
	Err      error
	Sessions []Live
	Items    []Item
	Launched []Launched
	Dormant  []Dormant
	Waiting  []RemoteWaiting
}

// RemoteWaiting is a session on another host whose last reply asks the owner.
type RemoteWaiting struct {
	Session, Line, Reply string
}

// State is how the host stands for display: its session count, "checking", or why it cannot be reached.
func (r RemoteBoard) State() string {
	switch {
	case r.Err != nil:
		return "unreachable: " + r.Err.Error()
	case r.At.IsZero():
		return "checking"
	}
	return fmt.Sprintf("%d sessions", len(r.Sessions))
}

// RemoteKey names ref on host, so keys another host numbers on its own never meet this host's.
func RemoteKey(host, ref string) string { return host + "/" + ref }

// ParseStatus is the Status s names, Unknown when it names none.
func ParseStatus(s string) Status {
	for st := Unknown; st <= Moved; st++ {
		if st.String() == s {
			return st
		}
	}
	return Unknown
}

func (c *Core) remoteBoards() []RemoteBoard {
	if c.Remotes == nil {
		return nil
	}
	return c.Remotes.Boards()
}

// RemoteRef finds the host ref belongs to and the ref that host knows it by: a host-qualified key, else a remote session id or its prefix.
func (c *Core) RemoteRef(ref string) (host, rest string, ok bool) {
	boards := c.remoteBoards()
	for _, r := range boards {
		if after, found := strings.CutPrefix(strings.TrimPrefix(ref, "launch:"), r.Host+"/"); found {
			return r.Host, after, true
		}
	}
	for _, r := range boards {
		for _, s := range r.Sessions {
			if s.ID != "" && (s.ID == ref || len(ref) >= 6 && strings.HasPrefix(s.ID, ref)) {
				return r.Host, s.ID, true
			}
		}
	}
	return "", "", false
}

// RunRemote runs ym args on host.
func (c *Core) RunRemote(host string, args ...string) (string, error) {
	if c.Remotes == nil {
		return "", fmt.Errorf("%s: no remote hosts are wired", host)
	}
	return c.Remotes.Run(host, args...)
}

// remoteFind is the live session on another host that ref names.
func (c *Core) remoteFind(ref string) (Live, bool) {
	for _, r := range c.remoteBoards() {
		for _, s := range r.Sessions {
			if s.ID != "" && (s.ID == ref || len(ref) >= 6 && strings.HasPrefix(s.ID, ref)) {
				return s, true
			}
		}
	}
	return Live{}, false
}

// remoteItem runs an item command on the host id belongs to and returns that host's item; ok is false for a local id.
func (c *Core) remoteItem(id string, args ...string) (Item, Delivery, bool, error) {
	host, rest, ok := c.RemoteRef(id)
	if !ok {
		return Item{}, Queued, false, nil
	}
	var it Item
	for _, r := range c.remoteBoards() {
		for _, x := range r.Items {
			if x.Key() == id {
				it = x
			}
		}
	}
	if it.ID == "" {
		it = Item{ID: id, Host: host}
	}
	out, err := c.RunRemote(host, append([]string{"item", args[0], rest}, args[1:]...)...)
	if err != nil {
		return it, Queued, true, err
	}
	d := Queued
	for i := Typed; i <= Held; i++ {
		if strings.Contains(out, i.String()) {
			d = i
		}
	}
	return it, d, true, nil
}

// addRemotes puts every remote host's sessions, items and launches on the board beside this host's own.
func (b *Board) addRemotes(boards []RemoteBoard, parents map[string]string, items *[]Item, dormant *[]Dormant) {
	b.Remotes = boards
	for _, r := range boards {
		for _, s := range r.Sessions {
			if s.Project != "" && s.Project != s.Key() {
				parents[s.Key()] = s.Project
			}
			b.Sessions = append(b.Sessions, s)
		}
		*items = append(*items, r.Items...)
		*dormant = append(*dormant, r.Dormant...)
		b.Launched = append(b.Launched, r.Launched...)
	}
}

// remoteWaiting lists the waiting sessions remote hosts reported, pointing into b's sessions.
func (b *Board) remoteWaiting() []Need {
	var out []Need
	for _, r := range b.Remotes {
		for _, w := range r.Waiting {
			for i := range b.Sessions {
				if s := &b.Sessions[i]; s.Remote == r.Host && s.Key() == w.Session {
					out = append(out, Need{Session: s, Project: s.Project, Reply: w.Reply, Line: w.Line})
				}
			}
		}
	}
	return out
}
