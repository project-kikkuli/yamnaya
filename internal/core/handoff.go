package core

import (
	"fmt"
	"time"
)

type HandoffInspector interface {
	InspectHandoff(Session) (time.Time, error)
	QuotaDialog(string) bool
}

func (c *Core) InspectHandoff(s Session) (time.Time, error) {
	h := c.harness(s.Harness)
	i, ok := h.(HandoffInspector)
	if !ok {
		return time.Time{}, fmt.Errorf("native quota-stop inspection unavailable")
	}
	if s.Prompt != nil {
		return time.Time{}, fmt.Errorf("native prompt is active")
	}
	handle, host, why := c.locate(s.TTY)
	if host == nil || why != "" {
		return time.Time{}, fmt.Errorf("cannot inspect native quota dialog: %s", why)
	}
	screen, err := host.Screen(handle)
	if err != nil {
		return time.Time{}, fmt.Errorf("cannot read native quota dialog: %w", err)
	}
	if _, prompt := h.ParsePrompt(screen); prompt || !i.QuotaDialog(screen) {
		return time.Time{}, fmt.Errorf("native screen is not a stopped quota dialog")
	}
	return i.InspectHandoff(s)
}
