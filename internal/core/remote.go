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
	Profiles []LoginProfile
	Usage    *Fleet
	Host     string
	// At is when the host last answered; zero while the first read is still running.
	At       time.Time
	Err      error
	Sessions []Live
	Items    []Item
	// Closed are its items answered or dismissed lately, with their history.
	Closed   []Item
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
	// Fresh is each of its managers' and chiefs' latest brief and whether it is behind, by its own reading.
	Fresh map[string]Freshness
	// Swollen are its processes holding a large share of its memory.
	Swollen []Swollen
	// Stale marks a board ym kept from an earlier read; At says when that host last answered.
	Stale bool
	// FailingSince is when reads of the host started failing, zero while the last read answered.
	FailingSince time.Time
}

// RemoteWaiting is a session on another host whose last reply asks the owner.
type RemoteWaiting struct {
	Session, Line, Reply string
}

// State is how the host stands for display: its session count, "checking", or why it cannot be reached.
func (r RemoteBoard) State() string { return r.StateAt(time.Now()) }

// SnapshotLate is how old a host's board may get before the board says its ym serve has fallen behind; that serve writes one
// every few seconds.
const SnapshotLate = 2 * time.Minute

// Late reports whether the host answers with a board its ym serve took longer than SnapshotLate before now.
func (r RemoteBoard) Late(now time.Time) bool {
	return r.Err == nil && !r.Stale && !r.At.IsZero() && now.Sub(r.At) > SnapshotLate
}

// StateAt is State as of now: how long a failing host has gone without an answer and when it last gave a good one, or how
// old an answering host's board is once its ym serve falls behind.
func (r RemoteBoard) StateAt(now time.Time) string {
	switch {
	case r.Err != nil && !r.At.IsZero() && !r.FailingSince.IsZero():
		return fmt.Sprintf("no answer for %s (last good %s): %s", Span(now.Sub(r.FailingSince)), r.At.Local().Format(time.Kitchen), r.Err.Error())
	case r.Err != nil && !r.At.IsZero():
		return fmt.Sprintf("no answer (last good %s): %s", r.At.Local().Format(time.Kitchen), r.Err.Error())
	case r.Err != nil:
		return "unreachable: " + r.Err.Error()
	case r.At.IsZero():
		return "checking"
	case r.Stale:
		return fmt.Sprintf("checking, last answered %s", r.At.Local().Format(time.Kitchen))
	case r.Late(now):
		return fmt.Sprintf("%d sessions, board %s old: its ym serve is behind", len(r.Sessions), Span(now.Sub(r.At)))
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

// shownRemotes are the other hosts' boards when this board shows them, the only time their managers join its org.
func (c *Core) shownRemotes() []RemoteBoard {
	if !c.ShowRemotes {
		return nil
	}
	return c.remoteBoards()
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
	if s, err := c.remoteFind(ref); err == nil {
		return s.Remote, s.ID, true
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
func (c *Core) remoteFind(ref string) (Live, error) {
	var sessions []Live
	var ids []string
	for _, r := range c.remoteBoards() {
		for _, s := range r.Sessions {
			s.Remote = r.Host
			sessions = append(sessions, s)
			ids = append(ids, s.ID)
		}
	}
	id, err := SessionRef(ref, ids)
	if err != nil {
		return Live{}, err
	}
	var found Live
	for _, s := range sessions {
		if s.ID == id {
			if found.ID != "" && found.Remote != s.Remote {
				return Live{}, fmt.Errorf("%w %q on %s and %s; qualify it with a host", ErrAmbiguous, ref, found.Remote, s.Remote)
			}
			found = s
		}
	}
	return found, nil
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
