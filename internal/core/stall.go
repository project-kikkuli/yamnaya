package core

import (
	"fmt"
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

// Hung reports whether a busy session has shown no activity for HungAfter.
func (s Live) Hung(now time.Time) bool {
	return s.Status == Busy && !s.lastActive().IsZero() && now.Sub(s.lastActive()) >= HungAfter
}

// noticeHung records each hung agent once per stall, with what it last did.
func (c *Core) noticeHung(b Board, o Org) error {
	now := c.now()
	for _, s := range b.Sessions {
		key := s.Key()
		if !s.Hung(now) {
			continue
		}
		if _, isManager := o.ManagerOf(key); isManager {
			continue
		}
		at := s.lastActive()
		text := fmt.Sprintf("agent %s looks hung: busy with no tool call, reply or subagent activity since %s.", Short(key), at.Local().Format(time.Kitchen))
		if s.Doing.Tool != "" {
			text += " Last doing " + s.Doing.String() + "."
		}
		text += fmt.Sprintf(" Look at it with ym open %s; end it with ym kill %s --yes and resume it with ym revive, or start a fresh agent.", key, key)
		if err := c.noticeAgent(b, o, key, EventAgentHung, text, "hung\x00"+key+"\x00"+at.UTC().Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	return nil
}
