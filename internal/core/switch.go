package core

import (
	"errors"
	"fmt"
	"github.com/project-kikkuli/yamnaya/internal/profile"
)

// Switch ports an idle session to the other harness and says how to resume it there.
func (c *Core) Switch(b Board, s Live, to string) (string, error) {
	return c.SwitchWithProfile(b, s, to, "")
}

func (c *Core) SwitchWithProfile(b Board, s Live, to, selected string) (string, error) {
	if s.Remote != "" {
		args := []string{"switch", s.Ref(), "--to", to}
		if selected != "" {
			args = append(args, "--profile", selected)
		}
		return c.RunRemote(s.Remote, args...)
	}
	if err := c.switchable(s, to); err != nil {
		return "", err
	}
	plan, err := c.preparePort(s.Session, to, selected)
	if err != nil {
		return "", err
	}
	defer plan.Close()
	id, err := plan.Write()
	if err != nil {
		return "", err
	}
	msg := fmt.Sprintf("ported to %s session %s", to, id)
	plan.Target.ID = id
	if ts := c.Targets(b, id, to); len(ts) > 0 && ts[0].Resume != nil {
		msg += fmt.Sprintf("\nquit the %s session, then run: %s", s.Harness, profile.Shell(func() []string {
			if plan.Target.ConfigRoot != "" {
				return c.resumeProfile(plan.Target)
			}
			return ts[0].Resume
		}()))
	}
	return msg, nil
}

// Relaunch ends an idle session, ports it to the other harness and resumes the port on ym's server in the same directory.
// Any failure from the kill on says the command that resumes the conversation by hand.
func (c *Core) Relaunch(s Live, to string) (string, error) { return c.RelaunchWithProfile(s, to, "") }

func (c *Core) RelaunchWithProfile(s Live, to, selected string) (string, error) {
	if s.Remote != "" {
		args := []string{"switch", s.Ref(), "--to", to, "--relaunch"}
		if selected != "" {
			args = append(args, "--profile", selected)
		}
		return c.RunRemote(s.Remote, args...)
	}
	if err := c.switchable(s, to); err != nil {
		return "", err
	}
	if c.Launches == nil {
		return "", errors.New("ym's launcher is unavailable")
	}
	if s.Cwd == "" {
		return "", fmt.Errorf("%s has no directory ym can read to relaunch it in", Short(s.Key()))
	}
	plan, err := c.preparePort(s.Session, to, selected)
	if err != nil {
		return "", err
	}
	defer plan.Close()
	if _, err := c.Kill(s); err != nil {
		return "", fmt.Errorf("%w; not relaunched; if it ended, resume it with: %s", err, c.resumeSessionIn(s.Session))
	}
	id, err := plan.Write()
	if err != nil {
		return "", fmt.Errorf("ended %s %s, but porting failed: %w; resume it with: %s", s.Harness, Short(s.Key()), err, c.resumeSessionIn(s.Session))
	}
	plan.Target.ID = id
	var msg string
	if scoped, ok := c.Launches.(interface {
		RelaunchProfile(Live, Session) (string, error)
	}); ok && plan.Target.ConfigRoot != "" {
		msg, err = scoped.RelaunchProfile(s, plan.Target)
	} else {
		msg, err = c.Launches.Relaunch(s, to, id)
	}
	if err != nil {
		return "", fmt.Errorf("ported to %s session %s, but starting it failed: %w; resume it with: %s", to, id, err, c.resumeSessionIn(plan.Target))
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
	if !s.Settled() {
		return fmt.Errorf("%s has an active tool or child session; switch once settled", Short(s.Key()))
	}
	if c.Port == nil && c.PreparePort == nil {
		return errors.New("no porter to switch harnesses with")
	}
	return nil
}

// Other is the harness a session would switch to.
func Other(harness string) string {
	if harness == "codex" {
		return "claude"
	}
	return "codex"
}

type PortPlan struct {
	Target Session
	Write  func() (string, error)
	Close  func()
}

func (c *Core) preparePort(s Session, to, selected string) (PortPlan, error) {
	if c.PreparePort != nil {
		return c.PreparePort(s, to, selected)
	}
	if selected != "" {
		return PortPlan{}, errors.New("profile-aware porter unavailable")
	}
	return PortPlan{Target: Session{Harness: to, Cwd: s.Cwd}, Write: func() (string, error) { return c.Port(s.Cwd, s.ID, to) }, Close: func() {}}, nil
}

func (c *Core) resumeSessionIn(s Session) string {
	return "cd " + profile.Shell([]string{s.Cwd}) + " && " + profile.Shell(c.resumeProfile(s))
}
