package core

import (
	"database/sql"
	"errors"
	"regexp"
	"strings"
)

func prKind(kind string) bool {
	switch kind {
	case EventPROpened, EventPRTouched, EventCIRed, EventCIGreen, EventPRReady, EventReview, EventMerged, EventClosed, EventConflict:
		return true
	}
	return false
}

func agentKind(kind string) bool {
	switch kind {
	case EventFinished, EventIdleAgain, EventAgentStopped, EventAgentHung, EventAgentAdrift, EventAgentSwollen, EventAgentOnboarding, EventResumed, EventResumeFailed:
		return true
	}
	return false
}

func (c *Core) noticeForeign(b Board) error {
	statuses, err := c.Store.PRStatuses()
	if err != nil {
		return err
	}
	for _, s := range b.Sessions {
		if _, local := b.Org.Managed(s.Key(), b.parents); local {
			continue
		}
		w, err := c.remoteOwnerVia(s.Key(), func() map[string]string { return b.parents })
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		for _, pr := range s.AllPRs() {
			st, fetched := statuses[pr.URL]
			events := prEvents(pr.URL, pr.URL, "agent "+RemoteKey(c.SelfName(), Short(s.Key())), st, fetched, c.now())
			if st.Foreign() {
				events = []GoalEvent{{Kind: EventPRTouched, Key: "touched:" + pr.URL, Payload: "worked on another author's " + pr.URL}}
			}
			for _, e := range events {
				if err := c.recordForeign(w, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// noticeForeignAgent relays what happened to an agent another host's manager assigned here, once per key; no manager here would hear it.
func (c *Core) noticeForeignAgent(b Board, session, kind, text, key string) error {
	w, err := c.remoteOwnerVia(session, func() map[string]string { return b.parents })
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return c.recordForeign(w, GoalEvent{Kind: kind, Payload: c.foreignText(session, text), Key: key})
}

// foreignText is text about session as its origin reads it, with the session and its commands naming this host.
func (c *Core) foreignText(session, text string) string {
	self := c.SelfName()
	whole := regexp.MustCompile(`(^|[^\w/.-])` + regexp.QuoteMeta(session) + `($|[^\w/-])`)
	text = whole.ReplaceAllString(text, "${1}"+RemoteKey(self, session)+"${2}")
	return strings.NewReplacer(
		"agent "+Short(session)+" ", "agent "+RemoteKey(self, Short(session))+" ",
		"agent "+Short(session)+"'", "agent "+RemoteKey(self, Short(session))+"'",
		"ym revive ", "ym revive "+self+"/",
		"on this host", "on "+self,
		"end it with kill ", "end it on "+self+" with kill ",
	).Replace(text)
}

func (c *Core) recordForeign(w RemoteAssignment, e GoalEvent) error {
	e.Goal, e.Key, e.Created = RemoteKey(w.Origin, w.Goal), RemoteKey(w.Origin, w.Goal)+":"+e.Key, c.now()
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO ym_goal_events(goal,kind,payload,key,created) VALUES (?,?,?,?,?) ON CONFLICT DO NOTHING`, e.Goal, e.Kind, e.Payload, e.Key, stamp(e.Created))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return err
	}
	if _, err = tx.Exec(`INSERT INTO ym_relay(dest,kind,body,created) VALUES (?,?,?,?)`, w.Origin, RelayWork, jsonOf(WorkMessage{Op: "event", Work: w, Event: e}), c.now().UnixNano()); err != nil {
		return err
	}
	return tx.Commit()
}
