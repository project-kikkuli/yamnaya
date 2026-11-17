package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
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
	unlock, err := c.lockLaunchRequest(id)
	if err != nil {
		return Started{}, err
	}
	defer unlock()
	return c.startPendingLaunchLocked(id)
}

func (c *Core) lockLaunchRequest(id string) (func(), error) {
	var databasePath string
	if err := c.Store.db.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&databasePath); err != nil {
		return nil, err
	}
	if databasePath == "" {
		if c.StateDir == "" {
			return nil, errors.New("launch queue requires a durable state directory")
		}
		databasePath = filepath.Join(c.StateDir, "ym.db")
	}
	lock, err := os.OpenFile(filepath.Join(filepath.Dir(databasePath), "launch-"+activationID("request", "lock", id)+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, err
	}
	return func() { lock.Close() }, nil
}

func (c *Core) startPendingLaunchLocked(id string) (Started, error) {
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
		return st, errors.Join(err, saveErr, c.launchAttention(id, n, err.Error()))
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

func launchAttentionQuestion(id string) string { return "Launch " + id + " is blocked" }

func (c *Core) launchAttention(id string, n NewLaunch, reason string) error {
	items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen, ItemSnoozed}})
	if err != nil {
		return err
	}
	for _, it := range items {
		if it.Session == "ym-launch" && it.Question == launchAttentionQuestion(id) {
			return nil
		}
	}
	_, err = c.Store.AddItem(Item{Session: "ym-launch", Scope: n.Repo, Kind: KindChore, Question: launchAttentionQuestion(id), DoLine: "Inspect blocked launch", Impact: "agents", Rec: reason + ". Inspect ym launch list --json, then explicitly retry with ym launch retry " + id + " after checking prior native effects."})
	return err
}

func (c *Core) RetryLaunch(id string) (string, error) {
	unlock, err := c.lockLaunchRequest(id)
	if err != nil {
		return "", err
	}
	defer unlock()
	defer c.Store.invalidate()
	res, err := c.Store.db.Exec(`UPDATE ym_pending_launches SET state='pending',reason='',result='' WHERE id=? AND state='blocked'`, id)
	if err != nil {
		return "", err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return "", err
	}
	if changed != 1 {
		return "", fmt.Errorf("launch %s is not blocked", id)
	}
	items, err := c.Store.Items(ItemFilter{States: []string{ItemOpen, ItemSnoozed}})
	if err != nil {
		return "", err
	}
	for _, it := range items {
		if it.Session == "ym-launch" && it.Question == launchAttentionQuestion(id) {
			if _, err := c.Store.Transition(it.Key(), ItemDismissed, "owner explicitly retries the preserved launch request"); err != nil {
				return "", err
			}
		}
	}
	st, err := c.startPendingLaunchLocked(id)
	return st.Message, err
}
