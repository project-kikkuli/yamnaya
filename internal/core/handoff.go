package core

import (
	"fmt"
	"strings"
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

// AtQuotaStop reports whether s is showing its harness's quota stop right now: the dialog, or the footer an idle session shows.
func (c *Core) AtQuotaStop(s Live) bool {
	if s.Status != Waiting && s.Status != Idle || s.Prompt != nil || s.Remote != "" {
		return false
	}
	h := c.harness(s.Harness)
	i, ok := h.(HandoffInspector)
	if !ok {
		return false
	}
	handle, host, why := c.locate(s.TTY)
	if host == nil || why != "" {
		return false
	}
	screen, err := host.Screen(handle)
	if err != nil {
		return false
	}
	if _, prompt := h.ParsePrompt(screen); prompt {
		return false
	}
	if s.Status == Waiting && i.QuotaDialog(screen) {
		return true
	}
	if silent, ok := h.(SilentQuotaStopper); ok {
		_, stopped, _ := silent.SilentQuotaStop(s.Session, screen)
		return stopped
	}
	return false
}

// QuotaStop is a session held on its limit that only its screen shows: At dates the turn, Reset is what the screen says, zero when it
// says nothing, and SelfContinues means the harness carries on by itself when the limit resets. Unread is a screen that could not be read,
// which says nothing about whether a stop still shows.
type QuotaStop struct {
	At, Reset     time.Time
	SelfContinues bool
	Unread        bool
}

// SilentQuotaStopper is a harness that can show a session held on its limit with no quota error behind it, or whose rows say otherwise.
type SilentQuotaStopper interface {
	SilentQuotaStop(s Session, screen string) (QuotaStop, bool, error)
}

// SilentQuotaStop reports a session its screen shows held on its limit though nothing else says so. stopped says the screen shows that; an
// error says what about it cannot be proved, as QuotaStopUnproven.
func (c *Core) SilentQuotaStop(s Session) (stop QuotaStop, stopped bool, err error) {
	i, ok := c.harness(s.Harness).(SilentQuotaStopper)
	if !ok || s.Prompt != nil {
		return QuotaStop{}, false, nil
	}
	handle, host, why := c.locate(s.TTY)
	if host == nil || why != "" {
		return QuotaStop{Unread: true}, false, nil
	}
	screen, err := host.Screen(handle)
	if err != nil {
		return QuotaStop{Unread: true}, false, nil
	}
	stop, stopped, err = i.SilentQuotaStop(s, screen)
	if err != nil {
		return stop, stopped, &QuotaStopUnproven{err}
	}
	return stop, stopped, nil
}

// QuotaStopUnproven is a session at the quota dialog whose turn ym cannot prove over. It never ends the turn by itself, so whoever
// waits on it needs telling, unlike a session waiting for any other reason.
type QuotaStopUnproven struct{ Cause error }

func (e *QuotaStopUnproven) Error() string { return e.Cause.Error() }
func (e *QuotaStopUnproven) Unwrap() error { return e.Cause }

// Drafting reports text sitting in s's input box, which someone may be typing.
func (c *Core) Drafting(s Live) bool {
	handle, host, why := c.locate(s.TTY)
	if host == nil || why != "" {
		return false
	}
	box, ok := readComposer(host, handle, c.harness(s.Harness), s.PID, true)
	return ok && strings.TrimSpace(box) != ""
}
