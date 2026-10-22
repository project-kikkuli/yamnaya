package core

import (
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"
	"time"
)

// Remotes reports what other hosts' ym boards show and carries ym commands to those hosts.
type Remotes interface {
	// Hosts names the remembered hosts without reading any of them.
	Hosts() []string
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
	// Self is what the host calls itself; Managers, Goals and ManagerSessions are its goal tree, Standing how each of its managers
	// and chiefs stands by its own reading.
	Self            string
	Managers        []Manager
	Goals           []Goal
	ManagerSessions map[string]string
	Standing        map[string]string
	// Feeds are its goals' news for the owner.
	Feeds map[string]Feed
	// Stale marks a board ym kept from an earlier read; At says when that host last answered.
	Stale bool
}

// RemoteWaiting is a session on another host whose last reply asks the owner.
type RemoteWaiting struct {
	Session, Line, Reply string
}

// State is how the host stands for display: its session count, "checking", or why it cannot be reached.
func (r RemoteBoard) State() string {
	switch {
	case r.Err != nil && !r.At.IsZero():
		return fmt.Sprintf("unreachable since %s: %s", r.At.Local().Format(time.Kitchen), r.Err.Error())
	case r.Err != nil:
		return "unreachable: " + r.Err.Error()
	case r.At.IsZero():
		return "checking"
	case r.Stale:
		return fmt.Sprintf("checking, last answered %s", r.At.Local().Format(time.Kitchen))
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

// RemoteRef splits a host-qualified key into its host and the ref that host knows it by, reading no host.
func (c *Core) RemoteRef(ref string) (host, rest string, ok bool) {
	var hosts []string
	if c.Remotes != nil {
		hosts = c.Remotes.Hosts()
	}
	for _, h := range append(hosts, c.peerHosts()...) {
		if after, found := strings.CutPrefix(strings.TrimPrefix(ref, "launch:"), h+"/"); found {
			return h, after, true
		}
	}
	return "", "", false
}

// RemoteSession is RemoteRef, else the host whose live session ref names by id or prefix; only that fallback reads the hosts.
func (c *Core) RemoteSession(ref string) (host, rest string, ok bool) {
	if host, rest, ok := c.RemoteRef(ref); ok {
		return host, rest, true
	}
	for _, r := range c.remoteBoards() {
		for _, s := range r.Sessions {
			if s.ID != "" && (s.ID == ref || len(ref) >= 6 && strings.HasPrefix(s.ID, ref)) {
				return r.Host, s.ID, true
			}
		}
	}
	return "", "", false
}

// ErrUnreachable is a host ssh could not reach, as against a ym command that failed there.
var ErrUnreachable = errors.New("host unreachable")

// RunRemote runs ym args on host.
func (c *Core) RunRemote(host string, args ...string) (string, error) {
	if c.Remotes == nil || !slices.Contains(c.Remotes.Hosts(), host) {
		return "", fmt.Errorf("%s: no remote hosts are wired", host)
	}
	return c.Remotes.Run(host, args...)
}

// RunOrRelay runs ym args on host now, or, when host cannot be reached now or is reached only through the host that carries this
// one's relays, queues them for ym serve to carry there.
func (c *Core) RunOrRelay(host string, args ...string) (string, error) {
	if c.Remotes != nil && slices.Contains(c.Remotes.Hosts(), host) {
		out, err := c.Remotes.Run(host, args...)
		if !errors.Is(err, ErrUnreachable) {
			return out, err
		}
	} else if !slices.Contains(c.peerHosts(), host) {
		return "", fmt.Errorf("%s: ym has not reached that host: %w", host, ErrNotFound)
	}
	if err := c.Relay(host, RelayYM, relayYM{Args: args}); err != nil {
		return "", err
	}
	return fmt.Sprintf("queued for %s; ym serve carries it there when it next reaches %s", host, host), nil
}

// remoteHost is the host a host-qualified id names when ym reaches that host directly or through relays.
func (c *Core) remoteHost(ref string) (host, rest string, ok bool) {
	host, rest, ok = SplitRemote(ref)
	if !ok {
		return "", ref, false
	}
	if c.Remotes != nil && slices.Contains(c.Remotes.Hosts(), host) || slices.Contains(c.peerHosts(), host) {
		return host, rest, true
	}
	return "", ref, false
}

// mergeRemoteOrg adds every remote host's managers, chiefs and goals to o under host-qualified ids, so chiefs and their reports
// meet in one tree across hosts, and returns how each remote one stands by its own host's reading.
func (c *Core) mergeRemoteOrg(o *Org, boards []RemoteBoard, feeds map[string]Feed) map[string]string {
	self := c.SelfName()
	q := func(host, ref string) string {
		if ref == "" {
			return ""
		}
		if h, rest, ok := SplitRemote(ref); ok {
			if h == self {
				return rest
			}
			return ref
		}
		return RemoteKey(host, ref)
	}
	standing := map[string]string{}
	for _, r := range boards {
		for _, m := range r.Managers {
			m.ID, m.ParentID, m.GoalID, m.Host = q(r.Host, m.ID), q(r.Host, m.ParentID), q(r.Host, m.GoalID), r.Host
			o.Managers[m.ID] = m
			if !m.Retired {
				o.order = append(o.order, m.ID)
			}
		}
		for session, id := range r.ManagerSessions {
			o.Sessions[session] = q(r.Host, id)
		}
		for _, g := range r.Goals {
			g.ID, g.ManagerID, g.Host = q(r.Host, g.ID), q(r.Host, g.ManagerID), r.Host
			o.Goals[g.ID] = g
		}
		for id, st := range r.Standing {
			standing[q(r.Host, id)] = st
		}
		for id, f := range r.Feeds {
			if feeds != nil {
				feeds[q(r.Host, id)] = f
			}
		}
	}
	return standing
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
	out, err := c.RunOrRelay(host, append([]string{"item", args[0], rest}, args[1:]...)...)
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
