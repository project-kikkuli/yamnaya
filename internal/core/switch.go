package core

import (
	"errors"
	"fmt"
	"strings"
)

// Switch ports an idle session to the other harness and says how to resume it there.
func (c *Core) Switch(b Board, s Live, to string) (string, error) {
	if err := c.switchable(s, to); err != nil {
		return "", err
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

// Relaunch ends an idle session, ports it to the other harness and resumes the port on ym's server in the same directory.
// Any failure from the kill on says the command that resumes the conversation by hand.
func (c *Core) Relaunch(s Live, to string) (string, error) {
	if err := c.switchable(s, to); err != nil {
		return "", err
	}
	if c.Launches == nil {
		return "", errors.New("ym's launcher is unavailable")
	}
	if s.Cwd == "" {
		return "", fmt.Errorf("%s has no directory ym can read to relaunch it in", Short(s.Key()))
	}
	if _, err := c.Kill(s); err != nil {
		return "", fmt.Errorf("%w; not relaunched; if it ended, resume it with: %s", err, c.resumeIn(s.Cwd, s.Harness, s.ID))
	}
	id, err := c.Port(s.Cwd, s.ID, to)
	if err != nil {
		return "", fmt.Errorf("ended %s %s, but porting failed: %w; resume it with: %s", s.Harness, Short(s.Key()), err, c.resumeIn(s.Cwd, s.Harness, s.ID))
	}
	msg, err := c.Launches.Relaunch(s, to, id)
	if err != nil {
		return "", fmt.Errorf("ported to %s session %s, but starting it failed: %w; resume it with: %s", to, id, err, c.resumeIn(s.Cwd, to, id))
	}
	return fmt.Sprintf("relaunched %s as %s %s: %s", Short(s.Key()), to, Short(id), msg), nil
}

func (c *Core) switchable(s Live, to string) error {
	if to != "claude" && to != "codex" {
		return fmt.Errorf("--to %q: want claude or codex", to)
	}
	if s.Harness == to {
		return fmt.Errorf("%s already runs %s", Short(s.Key()), to)
	}
	if s.Status != Idle {
		return fmt.Errorf("%s is %s; switch once it is idle", Short(s.Key()), s.Status)
	}
	if c.Port == nil {
		return errors.New("no porter to switch harnesses with")
	}
	return nil
}

func (c *Core) resumeIn(cwd, harnessName, id string) string {
	return "cd " + cwd + " && " + strings.Join(c.resume(harnessName, id), " ")
}

// Other is the harness a session would switch to.
func Other(harness string) string {
	if harness == "codex" {
		return "claude"
	}
	return "codex"
}
