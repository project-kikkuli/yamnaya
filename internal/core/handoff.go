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
	at, err := i.InspectHandoff(s)
	if err != nil {
		return at, &QuotaStopUnproven{err}
	}
	return at, nil
}

// QuotaStopUnproven is a session at the quota dialog whose turn ym cannot prove over. It never ends the turn by itself, so whoever
// waits on it needs telling, unlike a session waiting for any other reason.
type QuotaStopUnproven struct{ Cause error }

func (e *QuotaStopUnproven) Error() string { return e.Cause.Error() }
func (e *QuotaStopUnproven) Unwrap() error { return e.Cause }
