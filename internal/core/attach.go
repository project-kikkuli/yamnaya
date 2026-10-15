package core

import "os/exec"

// Attacher is a host that shows a terminal by taking over the caller's own terminal until the owner detaches.
type Attacher interface {
	Attach(h Handle) *exec.Cmd
}

// AttachCmd is the command that shows t in the caller's terminal, or nil when its host focuses in place instead.
func (c *Core) AttachCmd(t Target) *exec.Cmd {
	if t.Handle == nil {
		return nil
	}
	if a, ok := c.host(t.Handle.Host).(Attacher); ok {
		return a.Attach(*t.Handle)
	}
	return nil
}
