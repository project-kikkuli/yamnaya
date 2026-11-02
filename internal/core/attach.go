package core

import (
	"fmt"
	"os/exec"
	"strconv"
)

// Attacher is a host that shows a terminal by taking over the caller's own terminal until the owner detaches.
type Attacher interface {
	Attach(h Handle) (*exec.Cmd, error)
}

// AttachCmd is the command that shows t in the caller's terminal, or nil when its host focuses in place instead; an error when
// its host could not be read.
func (c *Core) AttachCmd(t Target) (*exec.Cmd, error) {
	if t.Remote != "" {
		if c.Remotes == nil {
			return nil, fmt.Errorf("%s: %w", t.Remote, ErrUnreachable)
		}
		args := []string{"open", "--no-board", t.Session.ExactRef()}
		if t.Pick > 0 {
			args = append(args, "--pick", strconv.Itoa(t.Pick))
		}
		return c.Remotes.Attach(t.Remote, args...), nil
	}
	if t.Handle == nil {
		return nil, nil
	}
	if a, ok := c.host(t.Handle.Host).(Attacher); ok {
		return a.Attach(*t.Handle)
	}
	return nil, nil
}

// Splitter is a host that shows a terminal in a new pane beside the caller's own, keeping the caller in view.
type Splitter interface {
	Split(h Handle) error
}

// OpenBeside shows t beside the caller's terminal; ErrNoHost when its host cannot.
func (c *Core) OpenBeside(t Target) error {
	if t.Remote != "" || t.Handle == nil {
		return ErrNoHost
	}
	s, ok := c.host(t.Handle.Host).(Splitter)
	if !ok {
		return ErrNoHost
	}
	return s.Split(*t.Handle)
}
