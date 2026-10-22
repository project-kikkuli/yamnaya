package core

import "time"

func freshSessions(sessions []Live, stops map[string]time.Time, observed time.Time) []Live {
	var out []Live
	for _, s := range sessions {
		if stop := stops[s.Key()]; stop.IsZero() || observed.After(stop) {
			out = append(out, s)
		}
	}
	return out
}

func (c *Core) StopSession(session string) error {
	if host, rest, ok := c.RemoteRef(session); ok {
		_, err := c.RunRemote(host, "kill", "--yes", rest)
		return err
	}
	if c.Store != nil {
		if err := c.Store.RecordKill(session, c.now()); err != nil {
			return err
		}
	}
	if l, ok := c.Launches.(interface{ SessionStopped(string) error }); ok {
		return l.SessionStopped(session)
	}
	return nil
}

func (c *Core) projectSessionStops(b *Board) error {
	stops, err := c.Store.Kills(time.Time{})
	if err != nil {
		return err
	}
	b.Sessions = freshSessions(b.Sessions, stops, b.At)
	for i := range b.Projects {
		p := &b.Projects[i]
		p.Sessions = freshSessions(p.Sessions, stops, b.At)
		var dormant []Dormant
		for _, d := range p.Dormant {
			if stop := stops[d.ID()]; stop.IsZero() || d.Started.After(stop) {
				dormant = append(dormant, d)
			}
		}
		p.Dormant = dormant
	}
	return nil
}
