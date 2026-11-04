package core

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// HungAfter is how long a busy session may go without a tool call, a reply or a busy subagent's activity before ym counts it hung;
// it is past the longest a single tool call may block.
const HungAfter = 15 * time.Minute

// lastActive is the latest sign the session itself did something: its turn starting, its latest tool call or reply, or a busy
// subagent's activity.
func (s Live) lastActive() time.Time {
	latest := s.Since
	for _, t := range []time.Time{s.Doing.At, s.ResultAt} {
		if t.After(latest) {
			latest = t
		}
	}
	for _, m := range s.Crew {
		if m.Status == Busy && m.Last.After(latest) {
			latest = m.Last
		}
	}
	return latest
}

// Hung reports whether a busy session has shown no activity for HungAfter while the host was awake since woke, no slot or lane
// held it up, and neither the owner's throttle nor a usage limit held it stopped.
func (s Live) Hung(now, woke time.Time) bool {
	if s.Paused || !s.QuotaAt.IsZero() {
		return false
	}
	quiet := s.lastActive()
	for _, t := range []time.Time{woke, s.Resumed, s.SlotAt} {
		if t.After(quiet) {
			quiet = t
		}
	}
	return s.Status == Busy && !s.lastActive().IsZero() && now.Sub(quiet) >= HungAfter
}

// noticeHung records each hung agent once per stall, with what it last did.
func (c *Core) noticeHung(b Board, o Org) error {
	now := c.now()
	for _, s := range b.Sessions {
		key := s.Key()
		if !s.Hung(now, c.Woke) {
			continue
		}
		if _, isManager := o.ManagerOf(key); isManager {
			continue
		}
		at := s.lastActive()
		ref := Short(s.ID)
		text := fmt.Sprintf("agent %s looks hung: no tool call, reply or subagent activity since %s. ym open %s shows it; ym kill %s --yes ends it, then ym revive or a fresh agent.", Short(key), at.Local().Format(time.Kitchen), ref, ref)
		if s.Doing.Tool != "" {
			text += " Last doing " + s.Doing.String() + "."
		}
		if err := c.noticeAgent(b, o, key, EventAgentHung, text, "hung\x00"+key+"\x00"+at.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

// limitHold is how long a session a usage limit stopped is left alone when ym cannot read when that limit resets.
const limitHold = time.Hour

var limitResets = regexp.MustCompile(`resets (?:([A-Z][a-z]{2} \d{1,2}), )?(\d{1,2}(?::\d{2})?[ap]m) \(([^)]+)\)`)

// limitLine is the harness's own line about s's usage limit, "" when the stop wrote none.
func (s Live) limitLine() string {
	if s.QuotaAt.IsZero() || s.ResultAt.Before(s.QuotaAt) {
		return ""
	}
	return oneLine(s.Result)
}

// resetIn reads when a limit line says the limit resets: "resets Nov 5, 3am (UTC)", or "resets 1:20am (UTC)" for the next such time after at.
func resetIn(line string, at time.Time) time.Time {
	m := limitResets.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}
	}
	loc, err := time.LoadLocation(m[3])
	if err != nil {
		return time.Time{}
	}
	clock := m[2]
	layout := "3pm"
	if strings.Contains(clock, ":") {
		layout = "3:04pm"
	}
	t, err := time.ParseInLocation(layout, clock, loc)
	if err != nil {
		return time.Time{}
	}
	at = at.In(loc)
	if m[1] != "" {
		day, err := time.ParseInLocation("Jan 2", m[1], loc)
		if err != nil {
			return time.Time{}
		}
		reset := time.Date(at.Year(), day.Month(), day.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		if reset.Before(at) {
			reset = reset.AddDate(1, 0, 0)
		}
		return reset
	}
	reset := time.Date(at.Year(), at.Month(), at.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	if !reset.After(at) {
		reset = reset.AddDate(0, 0, 1)
	}
	return reset
}

// limitUntil is when the usage limit that stopped s resets: the later of what its own line says and its account's full windows, or
// limitHold past the stop when neither says. A full window stays full until it resets, however long ago ym read it.
func (s Live) limitUntil(limits []Limit, now time.Time) time.Time {
	until := resetIn(s.limitLine(), s.QuotaAt)
	for _, w := range limits {
		if s.loginWindow(w) && w.Used >= 100 && w.ResetsAt.After(now) && w.ResetsAt.After(until) {
			until = w.ResetsAt
		}
	}
	if until.IsZero() {
		return s.QuotaAt.Add(limitHold)
	}
	return until
}

// loginWindow reports whether w limits s's whole login rather than one model.
func (s Live) loginWindow(w Limit) bool {
	if w.Harness != s.Harness || s.Harness == "codex" && !strings.HasPrefix(w.Name, "codex ") || s.Harness == "claude" && w.Name != "5h" && w.Name != "7d" {
		return false
	}
	if w.Account == "" {
		return w.Profile == s.Profile
	}
	return w.Account == s.Account
}

// limited reports whether s sits stopped by a usage limit that has not reset, so no message can move it.
func (c *Core) limited(b Board, s Live) bool {
	return !s.QuotaAt.IsZero() && c.now().Before(s.limitUntil(b.Fleet.Limits, c.now()))
}

// HoldForLimit queues text for the session ref names when a usage limit has it stopped, and reports whether it did.
func (c *Core) HoldForLimit(ref, text string) (bool, error) {
	found, err := c.Find(ref)
	if err != nil || found.QuotaAt.IsZero() {
		return false, nil
	}
	b, err := c.Board()
	if err != nil {
		return false, err
	}
	s, ok := b.Session(found.Key())
	if !ok || !c.limited(b, s) {
		return false, nil
	}
	_, err = c.Store.AddMessage(s.Key(), text, MsgPending)
	return err == nil, err
}

// noticeLimited records each session a usage limit has stopped once per stop, on the goal of the manager over it.
func (c *Core) noticeLimited(b Board, o Org) error {
	now := c.now()
	for _, s := range b.Sessions {
		if !c.limited(b, s) {
			continue
		}
		key := s.Key()
		who := "agent " + Short(key)
		if m, isManager := o.ManagerOf(key); isManager {
			if _, hasGoal := o.Goals[m.GoalID]; !hasGoal {
				continue
			}
			who = "manager " + m.Shown()
		} else if s.Title != "" {
			who += " (" + oneLine(s.Title) + ")"
		}
		why := s.limitLine()
		if why == "" {
			why = "usage limit, resets by " + s.limitUntil(b.Fleet.Limits, now).Local().Format("Jan 2 3:04PM")
		}
		text := fmt.Sprintf("%s stopped: %s. ym types nothing to it until then; message it after, or give its work to another login.", who, why)
		if err := c.noticeAgent(b, o, key, EventAgentLimited, text, "limited\x00"+key+"\x00"+s.QuotaAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}

// noticeSwollen tells the manager of each session running a process past SwollenShare of the machine's memory, once per process.
func (c *Core) noticeSwollen(b Board, o Org) error {
	for _, s := range b.Machine.Swollen {
		if s.Session == "" {
			continue
		}
		text := s.Line() + fmt.Sprintf(". It starves every session on this host as it grows. Look at it with ym open %s; end it with kill %d if it is a leak.",
			s.Session, s.PID)
		started := c.now().Add(-s.Elapsed).Truncate(time.Minute).UTC().Format(time.RFC3339)
		if err := c.noticeAgent(b, o, s.Session, EventAgentSwollen, text, fmt.Sprintf("swollen\x00%d\x00%s", s.PID, started)); err != nil {
			return err
		}
	}
	return nil
}
