package core

import (
	"errors"
	"fmt"
	"strings"
)

// Switch ports an idle session to the other harness and says how to resume it there.
func (c *Core) Switch(b Board, s Live, to string) (string, error) {
	if to != "claude" && to != "codex" {
		return "", fmt.Errorf("--to %q: want claude or codex", to)
	}
	if s.Harness == to {
		return "", fmt.Errorf("%s already runs %s", Short(s.Key()), to)
	}
	if s.Status != Idle {
		return "", fmt.Errorf("%s is %s; switch once it is idle", Short(s.Key()), s.Status)
	}
	if c.Port == nil {
		return "", errors.New("no porter to switch harnesses with")
	}
	id, err := c.Port(s.Cwd, s.ID, to)
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("ported to %s session %s", to, id)
	if ts := c.Targets(b, id, to); len(ts) > 0 && ts[0].Resume != nil {
		msg += fmt.Sprintf("\nquit the %s session, then run: %s", s.Harness, strings.Join(ts[0].Resume, " "))
	}
	return msg, nil
}

// Other is the harness a session would switch to.
func Other(harness string) string {
	if harness == "codex" {
		return "claude"
	}
	return "codex"
}
