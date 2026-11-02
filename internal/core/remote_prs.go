package core

import (
	"database/sql"
	"errors"
)

func prKind(kind string) bool {
	switch kind {
	case EventPROpened, EventPRTouched, EventCIRed, EventCIGreen, EventReview, EventMerged, EventClosed, EventConflict:
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
		w, err := c.remoteOwner(s.Key())
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		places := s.prPlaces()
		for _, pr := range s.AllPRs() {
			st, fetched := statuses[pr.URL]
			p := places[pr.URL]
			if !fetched || !pushedPR(p.Facts, pr, st) && !ownsPR(p.Worktree, p.Branch, p.Ahead, st, c.now()) {
				continue
			}
			events := prEvents(pr.URL, pr.URL, "agent "+RemoteKey(c.SelfName(), s.Key()), st, true)
			if st.Foreign() {
				events = []GoalEvent{{Kind: EventPRTouched, Key: "touched:" + pr.URL, Payload: "worked on another author's " + pr.URL}}
			}
			for _, e := range events {
				e.Goal, e.Key, e.Created = RemoteKey(w.Origin, w.Goal), RemoteKey(w.Origin, w.Goal)+":"+e.Key, c.now()
				if err := c.recordForeignPR(w, e); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (c *Core) recordForeignPR(w RemoteAssignment, e GoalEvent) error {
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
