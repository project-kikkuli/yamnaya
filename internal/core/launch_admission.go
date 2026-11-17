package core

import (
	"encoding/json"
	"errors"
	"fmt"
)

const launchAdmissionSchema = `
CREATE TABLE IF NOT EXISTS ym_pending_launches (
 id TEXT PRIMARY KEY, body TEXT NOT NULL, state TEXT NOT NULL,
 reason TEXT NOT NULL DEFAULT '', result TEXT NOT NULL DEFAULT '', created TEXT NOT NULL
);
`

var ErrLaunchDeferred = errors.New("launch awaits machine capacity")

type LaunchDeferred struct{ Reason string }

func (e *LaunchDeferred) Unwrap() error { return ErrLaunchDeferred }

func (e *LaunchDeferred) Error() string { return "queued for machine capacity: " + e.Reason }

func (c *Core) CheckLaunch(n NewLaunch) error {
	if c.LaunchAdmission == nil {
		return nil
	}
	return c.LaunchAdmission(n)
}

type PendingLaunch struct {
	ID, State, Reason, Result string
	Launch                    NewLaunch
}

func (s *Store) PendingLaunches() ([]PendingLaunch, error) {
	rows, err := s.db.Query(`SELECT id,body,state,reason,result FROM ym_pending_launches WHERE state IN ('pending','blocked') ORDER BY created,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PendingLaunch
	for rows.Next() {
		var p PendingLaunch
		var body string
		if err := rows.Scan(&p.ID, &body, &p.State, &p.Reason, &p.Result); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &p.Launch); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (c *Core) queueLaunch(n NewLaunch) (Started, error) {
	defer c.Store.invalidate()
	n.Request = activationID(n.Parent, "launch", n.Source)
	if n.Assignment != "" {
		n.Request = n.Assignment
	}
	_, err := c.Store.db.Exec(`INSERT INTO ym_pending_launches(id,body,state,created) VALUES(?,?,'pending',?) ON CONFLICT(id) DO NOTHING`, n.Request, jsonOf(n), stamp(c.now()))
	if err != nil {
		return Started{}, err
	}
	return c.startPendingLaunch(n.Request)
}

func (c *Core) startPendingLaunch(id string) (Started, error) {
	defer c.Store.invalidate()
	var n NewLaunch
	var body, state, result string
	if err := c.Store.db.QueryRow(`SELECT body,state,result FROM ym_pending_launches WHERE id=?`, id).Scan(&body, &state, &result); err != nil {
		return Started{}, err
	}
	if state == "started" || state == "cancelled" {
		return Started{Message: result}, nil
	}
	if state == "blocked" {
		return Started{}, fmt.Errorf("launch %s is blocked: %s", id, result)
	}
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		return Started{}, err
	}
	if n.Assignment != "" {
		var allowed int
		if err := c.Store.db.QueryRow(`SELECT count(*) FROM ym_assignments a JOIN ym_goals g ON a.goal='goal'||g.num WHERE a.id=? AND a.state='outstanding' AND a.report=0 AND g.state NOT IN ('done','dropped','killed')`, n.Assignment).Scan(&allowed); err != nil {
			return Started{}, err
		}
		if allowed == 0 {
			_, err := c.Store.db.Exec(`UPDATE ym_pending_launches SET state='cancelled',result='assignment no longer outstanding' WHERE id=?`, id)
			return Started{Message: "assignment no longer outstanding"}, err
		}
	}
	err := c.CheckLaunch(n)
	var st Started
	if err == nil {
		if c.Launches == nil {
			err = errors.New("ym's launcher is unavailable")
		} else {
			st, err = c.Launches.Start(n)
		}
	}
	var deferred *LaunchDeferred
	if errors.As(err, &deferred) {
		_, saveErr := c.Store.db.Exec(`UPDATE ym_pending_launches SET reason=? WHERE id=? AND state='pending'`, deferred.Reason, id)
		return Started{Message: "queued launch " + id + ": " + deferred.Reason}, saveErr
	}
	if err != nil {
		_, saveErr := c.Store.db.Exec(`UPDATE ym_pending_launches SET state='blocked',result=? WHERE id=?`, err.Error(), id)
		return st, errors.Join(err, saveErr)
	}
	if n.Assignment != "" && st.Session != "" {
		if err := c.SettleAssignment(n.Assignment, st.Session, n.Parent); err != nil {
			return st, err
		}
	}
	if n.Parent != "" && st.Session != "" {
		if err := c.Store.Link(st.Session, n.Parent); err != nil {
			return st, err
		}
	}
	_, err = c.Store.db.Exec(`UPDATE ym_pending_launches SET state='started',reason='',result=? WHERE id=?`, st.Message, id)
	return st, err
}

func (c *Core) DrainLaunches() error {
	pending, err := c.Store.PendingLaunches()
	if err != nil {
		return err
	}
	for _, p := range pending {
		if p.State != "pending" {
			continue
		}
		if err := c.CheckLaunch(p.Launch); err != nil {
			var deferred *LaunchDeferred
			if errors.As(err, &deferred) {
				return nil
			}
			return err
		}
		if _, err := c.startPendingLaunch(p.ID); err != nil {
			return err
		}
		return nil
	}
	return nil
}
