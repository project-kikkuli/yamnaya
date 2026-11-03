package core

import (
	"errors"
	"fmt"
	"sort"
	"time"
)

// ArchivedGoal is one ended goal as the archived view shows it: how it ended, why, who ended it and when. ID is the goal as Host
// names it, and Reopens says ym revive brings it back.
type ArchivedGoal struct {
	Host        string    `json:"host,omitempty"`
	ID          string    `json:"id"`
	Ask         string    `json:"ask"`
	State       string    `json:"state"`
	Disposition string    `json:"disposition"`
	Reason      string    `json:"reason,omitempty"`
	Actor       string    `json:"actor,omitempty"`
	At          time.Time `json:"at"`
	Reopens     bool      `json:"reopens"`
}

func archivedGoal(host string, g Goal) ArchivedGoal {
	a := ArchivedGoal{Host: host, ID: g.ID, Ask: g.Ask, State: g.State, Disposition: g.State, At: g.DoneAt, Reopens: g.Reopenable()}
	switch {
	case g.Closure != nil:
		a.Disposition, a.Reason, a.Actor, a.At = g.Closure.Disposition, g.Closure.Reason, g.Closure.Actor, g.Closure.At
	case g.State == GoalDropped:
		a.Reason = g.Evidence
	case g.State == GoalDone:
		a.Reason = g.Summary
	}
	return a
}

// ArchivedGoals lists every ended goal on this host and every other host's board, newest first.
func (b Board) ArchivedGoals() []ArchivedGoal {
	var out []ArchivedGoal
	for _, g := range b.Org.Goals {
		if g.Ended() && g.Host == "" {
			out = append(out, archivedGoal("", g))
		}
	}
	for _, r := range b.Remotes {
		for _, g := range r.Goals {
			if g.Ended() && g.Host == "" {
				out = append(out, archivedGoal(r.Host, g))
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Host+out[i].ID < out[j].Host+out[j].ID
	})
	return out
}

// lastDirected is when the goal last got an update, a brief, a steer or an assignment; nudges and other events do not count.
func (s *Store) lastDirected(g Goal, m Manager) (time.Time, error) {
	var last time.Time
	for _, q := range []struct{ query, arg string }{
		{`SELECT created FROM ym_goal_events WHERE goal = ? AND kind IN ('` + EventUpdate + `', '` + EventSteered + `')`, g.ID},
		{`SELECT created_at FROM ym_assignments WHERE goal = ?`, g.ID},
		{`SELECT created_at FROM ym_briefs WHERE manager = ?`, m.ID},
		{`SELECT created_at FROM ym_steers WHERE manager = ?`, m.ID},
	} {
		at, err := s.latest(q.query, q.arg, true)
		if err != nil {
			return last, err
		}
		if at.After(last) {
			last = at
		}
	}
	return last, nil
}

// archiveDone ends the sessions a verified done goal left behind, once DoneShown has passed since it finished and since anyone
// last directed it. The goal stays done.
func (c *Core) archiveDone(b Board) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	now := c.now()
	var errs []error
	for _, g := range o.Goals {
		m, ok := o.Managers[g.ManagerID]
		if !ok || g.State != GoalDone || now.Sub(g.DoneAt) < DoneShown {
			continue
		}
		_, targets, err := c.KillTargets(b, m.ID)
		if err != nil || len(targets) == 0 {
			errs = append(errs, err)
			continue
		}
		last, err := c.Store.lastDirected(g, m)
		if err != nil || now.Sub(last) < DoneShown {
			errs = append(errs, err)
			continue
		}
		toll := c.Weigh(targets)
		if err := c.teardown(m, g.ID, targets); err != nil {
			errs = append(errs, fmt.Errorf("archive %s: %w", g.ID, err))
			continue
		}
		_, _, err = c.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventGoalArchived, Created: now, Delivered: now,
			Payload: fmt.Sprintf("%s done and quiet for %s, so its leftover sessions ended: %s; ym revive %s brings any session back by its key", g.ID, DoneShown, toll, g.ID)})
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
