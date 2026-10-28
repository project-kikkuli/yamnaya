package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// DropGoalPR unties the pull request at url from the goal ref names for good, so it never holds up the goal's verification.
// Only the goal's own manager, or the owner when session is empty, may drop one; tied reports whether the goal had it.
func (c *Core) DropGoalPR(session, ref, url, why string) (e GoalEvent, tied bool, err error) {
	prs := PRsIn(url)
	if len(prs) != 1 || prs[0].URL != strings.TrimSpace(url) {
		return e, false, fmt.Errorf("%q is not a GitHub pull request link", url)
	}
	url = prs[0].URL
	if why = oneLine(why); why == "" {
		return e, false, errors.New("say why with --why")
	}
	g, err := c.GoalByRef(ref)
	if err != nil {
		return e, false, err
	}
	who := "the owner"
	if session != "" {
		o, err := c.LoadOrg()
		if err != nil {
			return e, false, err
		}
		m, ok := o.ManagerOf(c.Resolve(session))
		if !ok || m.GoalID != g.ID {
			return e, false, fmt.Errorf("only %s's manager %s or the owner may drop its pull requests", g.ID, g.ManagerID)
		}
		who = "manager " + m.ID
	}
	owned, err := c.Store.GoalPRs()
	if err != nil {
		return e, false, err
	}
	touched, err := c.Store.TouchedPRs()
	if err != nil {
		return e, false, err
	}
	tied = slices.Contains(owned[g.ID], url) || slices.Contains(touched[g.ID], url)
	now := c.now()
	e = GoalEvent{Goal: g.ID, Kind: EventPRDropped, Key: g.ID + ":dropped:" + url, Created: now,
		Payload: fmt.Sprintf("%s dropped %s from the goal: %s", who, url, why)}
	if session != "" {
		e.Delivered = now
	}
	if added, ok, err := c.Store.AddGoalEvent(e); err != nil || ok {
		return added, tied, err
	}
	return e, tied, nil
}
