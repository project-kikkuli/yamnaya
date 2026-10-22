package core

import (
	"fmt"
	"strings"
)

// noticeExits tells the manager or chief over each agent whose launched process stopped, once per agent and way it stopped: an
// agent that dies takes no idle turn, so nothing else would reach them.
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
		if _, managed := o.Managed(l.Session, b.parents); !managed {
			continue
		}
		to := o.Recipient(l.Session, b.parents)
		if to == Owner {
			continue
		}
		id := "exit\x00" + l.Session + "\x00" + l.State
		var seen int
		if err := c.Store.db.QueryRow(`SELECT count(*) FROM ym_forwarded WHERE id = ?`, id).Scan(&seen); err != nil {
			return err
		}
		if seen > 0 {
			continue
		}
		text := fmt.Sprintf("ym: agent %s (%s) stopped running: its process %s. Its worktree %s keeps what it wrote. Resume it with ym revive %s, or start a fresh agent with ym new.",
			Short(l.Session), l.Key, exitText(l.State), l.Path, l.Key)
		if err := c.tell(b, o.Managers[to].Session, text, false); err != nil {
			return err
		}
		if _, err := c.Store.db.Exec(`INSERT INTO ym_forwarded (id, at) VALUES (?, ?)`, id, now()); err != nil {
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
