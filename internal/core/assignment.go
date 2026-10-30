package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Assignment struct {
	ID             string
	Goal           string
	Session        string
	Generation     int
	Source         string
	Reason         string
	State          string
	Report         int64
	AcceptedReport int64
}

const assignmentCols = `id,goal,session,generation,source,reason,state,report,accepted_report`

func scanAssignment(row interface{ Scan(...any) error }) (Assignment, error) {
	var a Assignment
	err := row.Scan(&a.ID, &a.Goal, &a.Session, &a.Generation, &a.Source, &a.Reason, &a.State, &a.Report, &a.AcceptedReport)
	return a, err
}

func (s *Store) Assignments(goal string) ([]Assignment, error) {
	rows, err := s.db.Query(`SELECT `+assignmentCols+` FROM ym_assignments WHERE goal=? ORDER BY created_at,id`, goal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) assign(parent, worker, source, reason string, at time.Time) (Assignment, bool, error) {
	defer s.invalidate()
	if strings.TrimSpace(source) == "" || strings.TrimSpace(reason) == "" || strings.TrimSpace(worker) == "" {
		return Assignment{}, false, errors.New("assignment needs a worker, stable --source and --reason")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Assignment{}, false, err
	}
	defer tx.Rollback()
	goal, err := historicalGoalIn(tx, parent)
	if err != nil || goal == "" {
		return Assignment{}, false, err
	}
	if a, err := scanAssignment(tx.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE goal=? AND source=?`, goal, source)); err == nil {
		return a, false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return a, false, err
	}
	if _, err = workIn(tx, goal, "assignment:"+source, reason, at); err != nil {
		return Assignment{}, false, err
	}
	a := Assignment{ID: uuid.NewString(), Goal: goal, Session: worker, Source: source, Reason: reason, State: "outstanding"}
	err = tx.QueryRow(`INSERT INTO ym_assignments (id,goal,session,generation,source,reason,created_at)
 VALUES (?,?,?,(SELECT coalesce(max(generation),0)+1 FROM ym_assignments WHERE session=?),?,?,?) RETURNING generation`, a.ID, goal, worker, worker, source, reason, stamp(at)).Scan(&a.Generation)
	if err != nil {
		return a, false, err
	}
	return a, true, tx.Commit()
}

func (c *Core) AssignWork(parent, worker, source, reason string) (Assignment, error) {
	a, _, err := c.Store.assign(c.Resolve(parent), c.Resolve(worker), source, reason, c.now())
	return a, err
}

func (c *Core) ResumeGoal(session, source, reason string) error {
	o, err := c.LoadOrg()
	if err != nil {
		return err
	}
	g, err := o.goalOf(c.Resolve(session))
	if err != nil {
		return err
	}
	return c.Store.ResumeGoal(g.ID, source, reason, c.now())
}

func (c *Core) ReportAssignment(session, id, summary string) (int64, error) {
	defer c.Store.invalidate()
	if strings.TrimSpace(summary) == "" {
		return 0, errors.New("a terminal report needs --summary")
	}
	tx, err := c.Store.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	a, err := scanAssignment(tx.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, id))
	if err != nil {
		return 0, err
	}
	caller := c.Resolve(session)
	goal, err := historicalGoalIn(tx, caller)
	if err != nil {
		return 0, err
	}
	var manager string
	_ = tx.QueryRow(`SELECT manager FROM ym_manager_sessions WHERE session=?`, caller).Scan(&manager)
	g, err := goalIn(tx, a.Goal)
	if err != nil {
		return 0, err
	}
	if caller != a.Session && (goal != a.Goal || manager != g.ManagerID) {
		return 0, errors.New("only the assigned worker or its goal's manager may report terminal work")
	}
	if a.Report != 0 {
		return a.Report, nil
	}
	if a.State != "outstanding" {
		return 0, errors.New("assignment is already accepted")
	}
	var seq int64
	err = tx.QueryRow(`INSERT INTO ym_goal_events (goal,kind,payload,key,created) VALUES (?,?,?,?,?) RETURNING seq`, a.Goal, EventFinished,
		fmt.Sprintf("assignment %s generation %d terminal report from %s: %s; manager: ym goal accept %s --report <this-event>", a.ID, a.Generation, Short(a.Session), summary, a.ID), "assignment-report:"+a.ID, stamp(c.now())).Scan(&seq)
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE ym_assignments SET report=? WHERE id=?`, seq, id); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

func (c *Core) AcceptAssignment(session, id string, report int64) error {
	defer c.Store.invalidate()
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := scanAssignment(tx.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, id))
	if err != nil {
		return err
	}
	goal, err := historicalGoalIn(tx, c.Resolve(session))
	if err != nil {
		return err
	}
	g, err := goalIn(tx, a.Goal)
	if err != nil {
		return err
	}
	var manager string
	_ = tx.QueryRow(`SELECT manager FROM ym_manager_sessions WHERE session=?`, c.Resolve(session)).Scan(&manager)
	if goal != a.Goal || manager != g.ManagerID {
		return errors.New("only the goal's manager may accept terminal reports")
	}
	if report == 0 || report != a.Report {
		return errors.New("accept needs the terminal report event for this assignment generation; use ym goal report first")
	}
	if a.State == "accepted" && a.AcceptedReport == report {
		return nil
	}
	res, err := tx.Exec(`UPDATE ym_assignments SET state='accepted',accepted_report=? WHERE id=? AND state='outstanding' AND report=?`, report, id, report)
	if err = changed(res, err); err != nil {
		return err
	}
	return tx.Commit()
}

func (c *Core) RegisterSpawn(child, parent string) error {
	if child == parent {
		return errors.New("a session cannot spawn itself")
	}
	if _, err := c.AssignWork(parent, child, "spawn:"+child+":"+parent, "registered spawned worker"); err != nil {
		return err
	}
	return c.Store.Link(child, parent)
}

func (c *Core) BindAssignment(session, id, worker string) error {
	defer c.Store.invalidate()
	tx, err := c.Store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := scanAssignment(tx.QueryRow(`SELECT `+assignmentCols+` FROM ym_assignments WHERE id=?`, id))
	if err != nil {
		return err
	}
	goal, err := historicalGoalIn(tx, c.Resolve(session))
	if err != nil {
		return err
	}
	if goal != a.Goal {
		return errors.New("only a session of the assigned goal may bind its worker")
	}
	if a.State != "outstanding" || a.Report != 0 {
		return errors.New("bind the worker before its terminal report")
	}
	if strings.TrimSpace(worker) == "" || worker == c.Resolve(session) {
		return errors.New("bind needs a worker session")
	}
	if a.Session != worker {
		if _, err = tx.Exec(`UPDATE ym_assignments SET session=?,generation=(SELECT coalesce(max(generation),0)+1 FROM ym_assignments WHERE session=?) WHERE id=?`, worker, worker, id); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO ym_links (child,parent) VALUES (?,?) ON CONFLICT(child) DO UPDATE SET parent=excluded.parent`, worker, c.Resolve(session)); err != nil {
		return err
	}
	return tx.Commit()
}
