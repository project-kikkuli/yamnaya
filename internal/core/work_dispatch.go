package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

var ErrWorkRevoked = errors.New("origin revoked this assignment's launch")

const workDispatchSchema = `CREATE TABLE IF NOT EXISTS ym_work_dispatches (
 id TEXT PRIMARY KEY, goal TEXT NOT NULL, body TEXT NOT NULL, state TEXT NOT NULL
);`

type WorkCourier interface {
	StartWork(from, host string, work RemoteAssignment) error
	AuthorizeWork(origin, id string, run func(RemoteAssignment) error) error
}

func publishAssignmentIn(tx *sql.Tx, origin, parent string, a Assignment, at time.Time) error {
	host, worker, remote := SplitRemote(a.Session)
	if origin == "" || !remote {
		return nil
	}
	g, err := goalIn(tx, a.Goal)
	if err != nil {
		return err
	}
	w := RemoteAssignment{Origin: origin, Host: host, Requester: origin, Goal: a.Goal, Manager: g.ManagerID, Parent: RemoteKey(origin, parent), Worker: worker, Assignment: a, State: "started"}
	if a.State == "accepted" {
		w.State = "accepted"
	}
	if _, err = tx.Exec(`INSERT INTO ym_work_dispatches(id,goal,body,state) VALUES (?,?,?,'mapped') ON CONFLICT DO NOTHING`, a.ID, a.Goal, jsonOf(w)); err != nil {
		return err
	}
	return workMessageIn(tx, host, WorkMessage{Op: "register", Work: w}, at)
}

func workMessageIn(tx *sql.Tx, host string, m WorkMessage, at time.Time) error {
	_, err := tx.Exec(`INSERT INTO ym_relay(dest,kind,body,created) VALUES (?,?,?,?)`, host, RelayWork, jsonOf(m), at.UnixNano())
	return err
}

func (c *Core) registerLaunch(w RemoteAssignment) error {
	origin := c.SelfName()
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := scanAssignment(tx.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, w.Assignment.ID))
	if err != nil {
		return err
	}
	host, worker, remote := SplitRemote(a.Session)
	if !remote || w.Origin != origin || a.Goal != w.Goal || host != w.Host || a.Generation != w.Assignment.Generation || w.Launch == nil {
		return errors.New("launch intent changed assignment ownership")
	}
	g, err := goalIn(tx, a.Goal)
	if err != nil {
		return err
	}
	w.Assignment, w.Worker, w.Manager = a, worker, g.ManagerID
	state := "pending"
	if g.Ended() || a.State != "outstanding" {
		state = "revoked"
	}
	if _, err = tx.Exec(`INSERT INTO ym_work_dispatches(id,goal,body,state) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, a.ID, a.Goal, jsonOf(w), state); err != nil {
		return err
	}
	if state == "revoked" {
		if err = workMessageIn(tx, w.Host, WorkMessage{Op: "revoke", Work: w}, c.now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) pendingLaunches() ([]RemoteAssignment, error) {
	rows, err := s.db.Query(`SELECT body FROM ym_work_dispatches WHERE state IN ('pending','dispatching') ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RemoteAssignment
	for rows.Next() {
		var body string
		var w RemoteAssignment
		if err = rows.Scan(&body); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(body), &w); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (c *Core) AuthorizeWork(id string, run func(RemoteAssignment) error) error {
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var body, state string
	if err = tx.QueryRow(`SELECT body,state FROM ym_work_dispatches WHERE id=?`, id).Scan(&body, &state); err != nil {
		return err
	}
	if state == "revoked" {
		return ErrWorkRevoked
	}
	if state == "started" {
		return nil
	}
	var w RemoteAssignment
	if err = json.Unmarshal([]byte(body), &w); err != nil {
		return err
	}
	a, err := scanAssignment(tx.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, id))
	if err != nil {
		return err
	}
	g, err := goalIn(tx, a.Goal)
	if err != nil {
		return err
	}
	if w.Launch == nil || g.Ended() || a.State != "outstanding" || a.Generation != w.Assignment.Generation {
		return ErrWorkRevoked
	}
	if _, err = tx.Exec(`UPDATE ym_work_dispatches SET state='dispatching' WHERE id=?`, id); err != nil {
		return err
	}
	dispatchErr := run(w)
	state = "started"
	if dispatchErr != nil {
		state = "pending"
		if errors.Is(dispatchErr, ErrFinal) {
			state = "failed"
			if _, err = assignmentReportIn(tx, a, "launch permanently refused: "+dispatchErr.Error(), c.now()); err != nil {
				return err
			}
			w.Launch = nil
			w.State = "failed"
			if err = workMessageIn(tx, w.Host, WorkMessage{Op: "register", Work: w}, c.now()); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(`UPDATE ym_work_dispatches SET state=? WHERE id=?`, state, id); err != nil {
		return err
	}
	return errors.Join(dispatchErr, tx.Commit())
}

func revokeWorkIn(tx *sql.Tx, goal string, at time.Time) error {
	rows, err := tx.Query(`SELECT body FROM ym_work_dispatches WHERE goal=? AND state!='revoked'`, goal)
	if err != nil {
		return err
	}
	var all []RemoteAssignment
	for rows.Next() {
		var body string
		var w RemoteAssignment
		if err = rows.Scan(&body); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal([]byte(body), &w); err != nil {
			rows.Close()
			return err
		}
		all = append(all, w)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, w := range all {
		if err = workMessageIn(tx, w.Host, WorkMessage{Op: "revoke", Work: w}, at); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`UPDATE ym_work_dispatches SET state='revoked' WHERE goal=?`, goal)
	return err
}

func (c *Core) dispatchOwn(hosts []string, courier WorkCourier) error {
	self := c.SelfName()
	pending, err := c.Store.pendingLaunches()
	if err != nil {
		return err
	}
	for _, w := range pending {
		if !slices.Contains(hosts, w.Host) {
			continue
		}
		if err = c.AuthorizeWork(w.Assignment.ID, func(work RemoteAssignment) error { return courier.StartWork(self, work.Host, work) }); err != nil && !errors.Is(err, ErrWorkRevoked) && !errors.Is(err, ErrUnreachable) {
			return err
		}
	}
	return nil
}

func (c *Core) dispatchCarried(origin string, pending []RemoteAssignment, hosts []string, courier WorkCourier) error {
	self := c.SelfName()
	for _, w := range pending {
		if w.Origin != origin || w.Host != c.SelfName() && !slices.Contains(hosts, w.Host) {
			continue
		}
		err := courier.AuthorizeWork(origin, w.Assignment.ID, func(work RemoteAssignment) error {
			if work.Host == c.SelfName() {
				return c.StartWork(work)
			}
			return courier.StartWork(self, work.Host, work)
		})
		if err != nil && !errors.Is(err, ErrWorkRevoked) && !errors.Is(err, ErrUnreachable) {
			return err
		}
	}
	return nil
}

func (c *Core) StartWork(w RemoteAssignment) error {
	if identity, err := c.Store.RelaySelf(); err != nil {
		return err
	} else if identity.Name == "" && w.Host != "" {
		if err = c.Store.setRelaySelf(RelaySelf{Name: w.Host, Courier: w.Origin, Peers: identity.Peers, At: c.now()}); err != nil {
			return err
		}
	}
	if w.Host != c.SelfName() || w.Origin == "" || w.Launch == nil {
		return errors.New("live dispatch lacks origin, destination or launch")
	}
	w.State = "approved"
	if err := c.Store.putRemoteWork(w); err != nil {
		return err
	}
	return c.launchForeign(w)
}

func (c *Core) revokeForeign(w RemoteAssignment) error {
	w.State = "revoked"
	if err := c.Store.putRemoteWork(w); err != nil {
		return err
	}
	if _, err := c.Store.db.Exec(`UPDATE ym_remote_work SET state='revoked' WHERE id=?`, w.Assignment.ID); err != nil {
		return err
	}
	current, err := c.Store.remoteWork(w.Assignment.ID)
	if err != nil || c.Launches == nil || current.Host != c.SelfName() {
		return err
	}
	if ends, ok := c.Launches.(interface {
		EndAssignment(string, string) (string, error)
	}); ok {
		if _, err = ends.EndAssignment(w.Assignment.ID, w.Goal); !errors.Is(err, ErrNotFound) {
			return err
		}
	}
	_, err = c.Launches.End(current.Worker, w.Goal)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("cancel revoked assignment %s: %w", w.Assignment.ID, err)
	}
	return nil
}
