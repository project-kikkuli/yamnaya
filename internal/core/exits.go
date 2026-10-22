package core

import (
	"fmt"
	"strings"
)

// noticeExits records, once per agent and way it stopped, each agent whose launched process stopped: an agent that dies takes no
// idle turn, so nothing else would reach its manager.
func (c *Core) noticeExits(b Board, o Org) error {
	kills, err := c.Store.Kills(timeZero)
	if err != nil {
		return err
	}
	for _, l := range b.Launched {
		if !strings.HasPrefix(l.State, "exited") && !strings.HasPrefix(l.State, "sig") {
			continue
		}
		if _, killed := kills[l.Session]; killed || l.Session == "" {
			continue
		}
		if _, isManager := o.ManagerOf(l.Session); isManager {
			continue
		}
		text := fmt.Sprintf("agent %s (%s) stopped running: its process %s. Its worktree %s keeps what it wrote. Resume it with ym revive %s, or start a fresh agent with ym new.",
			Short(l.Session), l.Key, exitText(l.State), l.Path, l.Key)
		if err := c.noticeAgent(b, o, l.Session, EventAgentStopped, text, "exit\x00"+l.Session+"\x00"+l.State); err != nil {
			return err
		}
	}
	return nil
}

func exitText(state string) string {
	if sig, ok := strings.CutPrefix(state, "sig"); ok {
		return "was killed by signal " + sig
	}
	return strings.Replace(state, "exited", "exited with status", 1)
}
