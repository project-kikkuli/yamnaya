package core

import (
	"fmt"
	"strconv"
)

// onboard marks l Onboarding when its harness asked for a screen check and the screen shows the harness's first-run setup.
func onboard(l *Live, hn Harness, host Host, h Handle) bool {
	o, ok := hn.(Onboarder)
	if !ok || !l.CheckScreen {
		return false
	}
	screen, err := host.Screen(h)
	if err != nil {
		return false
	}
	step, ok := o.Onboarding(screen)
	if !ok {
		return false
	}
	l.Status, l.WaitingFor = Onboarding, step
	return true
}

// noticeOnboarding tells the manager of each agent held on first-run onboarding, once per process.
func (c *Core) noticeOnboarding(b Board, o Org) error {
	for _, s := range b.Sessions {
		if s.Status != Onboarding {
			continue
		}
		key := s.Key()
		text := fmt.Sprintf("agent %s sits on %s's first-run onboarding (%s) and takes no messages until the owner finishes it in ym open %s.",
			Short(key), s.Harness, s.WaitingFor, key)
		if err := c.noticeAgent(b, o, key, EventAgentOnboarding, text, "onboarding\x00"+key+"\x00"+strconv.Itoa(s.PID)); err != nil {
			return err
		}
	}
	return nil
}
