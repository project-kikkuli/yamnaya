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

// SilentQuotaStopper is a harness that can show a session held at its quota dialog with no quota error behind it.
type SilentQuotaStopper interface {
	SilentQuotaStop(s Session, screen string) (time.Time, bool, error)
}

// SilentQuotaStop reports a session held at the quota dialog though no turn hit the limit: it started on a used-up login. stopped says
// the dialog is up; an error says what about it cannot be proved, as QuotaStopUnproven.
func (c *Core) SilentQuotaStop(s Session) (at time.Time, stopped bool, err error) {
	i, ok := c.harness(s.Harness).(SilentQuotaStopper)
	if !ok || s.Prompt != nil {
		return time.Time{}, false, nil
	}
	handle, host, why := c.locate(s.TTY)
	if host == nil || why != "" {
		return time.Time{}, false, nil
	}
	screen, err := host.Screen(handle)
	if err != nil {
		return time.Time{}, false, nil
	}
	at, stopped, err = i.SilentQuotaStop(s, screen)
	if err != nil {
		return at, stopped, &QuotaStopUnproven{err}
	}
	return at, stopped, nil
}

// QuotaStopUnproven is a session at the quota dialog whose turn ym cannot prove over. It never ends the turn by itself, so whoever
// waits on it needs telling, unlike a session waiting for any other reason.
type QuotaStopUnproven struct{ Cause error }

func (e *QuotaStopUnproven) Error() string { return e.Cause.Error() }
func (e *QuotaStopUnproven) Unwrap() error { return e.Cause }
