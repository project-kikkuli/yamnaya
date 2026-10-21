package core

import (
	"strings"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/work"
)

func sessionMission(w work.Scope) bool {
	return w.Kind == "" && w.Owner == "" && w.Manager == "" && w.Parent == "" && strings.HasPrefix(w.Origin, ThisHost()+"/")
}

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

func (c *Core) projectSessionStops(b *Board, all []work.Scope) error {
	stops, err := c.Store.Kills(time.Time{})
	if err != nil {
		return err
	}
	resumed := map[string]bool{}
	for _, w := range all {
		for session, o := range w.Observations {
			resumed[session] = resumed[session] || o.LastLiveAt.After(stops[session])
		}
	}
	b.Sessions = freshSessions(b.Sessions, stops, b.At)
	live := map[string]bool{}
	for _, s := range b.Sessions {
		live[s.Key()] = true
	}
	for i := range b.Projects {
		p := &b.Projects[i]
		p.Sessions = freshSessions(p.Sessions, stops, b.At)
		var dormant []Dormant
		for _, d := range p.Dormant {
			if stops[d.ID()].IsZero() || resumed[d.ID()] {
				dormant = append(dormant, d)
			}
		}
		p.Dormant = dormant
		if p.Work == nil || !sessionMission(*p.Work) || len(p.Sessions) != 0 {
			continue
		}
		running := false
		for session := range p.Work.Bindings {
			running = running || live[session]
		}
		if running {
			continue
		}
		for session := range p.Work.Bindings {
			if !stops[session].IsZero() && !resumed[session] {
				p.Work.State = "archived"
				break
			}
		}
	}
	return nil
}
