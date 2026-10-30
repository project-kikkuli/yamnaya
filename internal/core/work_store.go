package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrStale = errors.New("goal work or completion changed; read current state before retrying")

const workSchema = `
CREATE TABLE IF NOT EXISTS ym_goal_work (
 goal TEXT NOT NULL, source TEXT NOT NULL, reason TEXT NOT NULL, created_at TEXT NOT NULL,
 PRIMARY KEY(goal, source)
);
CREATE TABLE IF NOT EXISTS ym_assignments (
 id TEXT PRIMARY KEY, goal TEXT NOT NULL, session TEXT NOT NULL, generation INTEGER NOT NULL,
 source TEXT NOT NULL, reason TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'outstanding',
 report INTEGER NOT NULL DEFAULT 0, accepted_report INTEGER NOT NULL DEFAULT 0,
 created_at TEXT NOT NULL, UNIQUE(goal, source)
);
CREATE INDEX IF NOT EXISTS ym_assignments_goal ON ym_assignments(goal, state);
`

func changed(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err == nil && n != 1 {
		return ErrStale
	}
	return err
}

func goalIn(tx *sql.Tx, id string) (Goal, error) {
	n, err := numOf(id, "goal")
	if err != nil {
		return Goal{}, err
	}
	return scanGoal(tx.QueryRow(`SELECT `+goalCols+` FROM ym_goals WHERE num=?`, n))
}

func scopeIn(tx *sql.Tx, g Goal) (string, error) {
	var text string
	err := tx.QueryRow(`SELECT coalesce((SELECT revision||':'||text FROM ym_steers WHERE manager=? ORDER BY revision DESC LIMIT 1),'')`, g.ManagerID).Scan(&text)
	return jsonOf([]string{g.Ask, g.ManagerID, text}), err
}

func prSnapshotIn(tx *sql.Tx, goal string, v Verification) (string, error) {
	rows, err := tx.Query(`WITH owned AS (
 SELECT substr(key,length(goal)+5) AS url FROM ym_goal_events WHERE goal=? AND kind='pr-opened'
 UNION SELECT value FROM json_each(?))
 SELECT o.url,coalesce(p.state,''),coalesce(p.head,''),coalesce(p.passed,0),coalesce(p.failed,0),coalesce(p.pending,0),coalesce(p.unresolved,0),coalesce(p.merge_details,'{}')
 FROM owned o LEFT JOIN ym_prs p ON p.url=o.url
 WHERE NOT EXISTS (SELECT 1 FROM ym_goal_events d WHERE d.goal=? AND d.kind='pr-dropped' AND d.key=d.goal||':dropped:'||o.url)
 AND NOT (coalesce(json_extract(p.merge_details,'$.Author'),'')!='' AND coalesce(json_extract(p.merge_details,'$.Viewer'),'')!='' AND lower(json_extract(p.merge_details,'$.Author'))!=lower(json_extract(p.merge_details,'$.Viewer')))
 ORDER BY o.url`, goal, jsonOf(prsOf(v)), goal)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var url, state, head, details string
		var passed, failed, pending, threads int
		if err := rows.Scan(&url, &state, &head, &passed, &failed, &pending, &threads, &details); err != nil {
			return "", err
		}
		out = append(out, jsonOf([]any{url, state, head, passed, failed, pending, threads, details}))
	}
	return jsonOf(out), rows.Err()
}

func eventIn(tx *sql.Tx, g, kind, payload, key string, at time.Time, told bool) error {
	var delivered any
	if told {
		delivered = stamp(at)
	}
	_, err := tx.Exec(`INSERT INTO ym_goal_events (goal,kind,payload,key,created,delivered_to_manager) VALUES (?,?,?,?,?,?) ON CONFLICT DO NOTHING`, g, kind, payload, key, stamp(at), delivered)
	return err
}

func outstandingIn(tx *sql.Tx, goal string) error {
	var n int
	if err := tx.QueryRow(`SELECT count(*) FROM ym_assignments WHERE goal=? AND state='outstanding'`, goal).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return fmt.Errorf("%s has %d outstanding assignments; inspect ym goal assignments and accept terminal reports with ym goal accept", goal, n)
	}
	return nil
}

func (s *Store) openCompletion(v Verification) (Goal, Verification, error) {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return Goal{}, v, err
	}
	defer tx.Rollback()
	g, err := goalIn(tx, v.Goal)
	if err != nil {
		return g, v, err
	}
	if v.recovery && (g.State != GoalVerifying || g.ActiveVerification != 0 || g.Version != v.expectedGoalVersion) {
		return g, v, ErrStale
	}
	if g.WorkRevision != v.WorkRevision {
		return g, v, ErrStale
	}
	if g.Ended() {
		return g, v, fmt.Errorf("%s is %s already", g.ID, g.State)
	}
	if err = outstandingIn(tx, g.ID); err != nil {
		return g, v, err
	}
	v.WorkRevision = g.WorkRevision
	if v.Scope, err = scopeIn(tx, g); err != nil {
		return g, v, err
	}
	if v.PRSnapshot, err = prSnapshotIn(tx, g.ID, v); err != nil {
		return g, v, err
	}
	if _, err = tx.Exec(`UPDATE ym_goal_verifications SET verdict='superseded', decided_at=?, version=version+1 WHERE goal=? AND verdict=''`, stamp(v.CreatedAt), g.ID); err != nil {
		return g, v, err
	}
	err = tx.QueryRow(`INSERT INTO ym_goal_verifications (goal,attempt,summary,evidence,created_at,work_revision,scope,pr_snapshot)
 VALUES (?,(SELECT coalesce(max(attempt),0)+1 FROM ym_goal_verifications WHERE goal=?),?,?,?,?,?,?) RETURNING num,attempt`, g.ID, g.ID, v.Summary, jsonOf(v.Evidence), stamp(v.CreatedAt), v.WorkRevision, v.Scope, v.PRSnapshot).Scan(&v.ID, &v.Attempt)
	if err != nil {
		return g, v, err
	}
	_, err = tx.Exec(`UPDATE ym_goals SET state='verifying',summary=?,evidence='',acked_at=NULL,active_verification=?,version=version+1 WHERE num=?`, v.Summary, v.ID, strings.TrimPrefix(g.ID, "goal"))
	if err != nil {
		return g, v, err
	}
	if err = eventIn(tx, g.ID, EventVerifying, fmt.Sprintf("attempt %d: %s", v.Attempt, v.Summary), fmt.Sprintf("completion:%d:open", v.ID), v.CreatedAt, true); err != nil {
		return g, v, err
	}
	g.State, g.Summary, g.Evidence, g.AckedAt, g.ActiveVerification = GoalVerifying, v.Summary, "", time.Time{}, v.ID
	g.Version++
	return g, v, tx.Commit()
}

func workIn(tx *sql.Tx, goal, source, reason string, at time.Time) (bool, error) {
	if strings.TrimSpace(source) == "" || strings.TrimSpace(reason) == "" {
		return false, errors.New("work needs a stable --source and a --reason")
	}
	g, err := goalIn(tx, goal)
	if err != nil {
		return false, err
	}
	if g.State == GoalDropped || g.State == GoalKilled {
		return false, fmt.Errorf("%s is %s; work cannot reopen it", goal, g.State)
	}
	res, err := tx.Exec(`INSERT INTO ym_goal_work (goal,source,reason,created_at) VALUES (?,?,?,?) ON CONFLICT DO NOTHING`, goal, source, reason, stamp(at))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE ym_goal_verifications SET verdict='superseded',decided_at=?,version=version+1 WHERE goal=? AND verdict=''`, stamp(at), goal); err != nil {
		return false, err
	}
	_, err = tx.Exec(`UPDATE ym_goals SET work_revision=work_revision+1,version=version+1,active_verification=0,
 state=CASE WHEN state IN ('done','verifying') THEN 'active' ELSE state END,
 summary='',evidence='',done_at=NULL,acked_at=NULL,last_moved_at=? WHERE num=?`, stamp(at), strings.TrimPrefix(goal, "goal"))
	if err != nil {
		return false, err
	}
	return true, eventIn(tx, goal, EventWorkResumed, reason, "work:"+goal+":"+source, at, true)
}

func (s *Store) ResumeGoal(goal, source, reason string, at time.Time) error {
	if strings.TrimSpace(source) == "" {
		return errors.New("resume needs a stable --source")
	}
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = workIn(tx, goal, "resume:"+source, reason, at); err != nil {
		return err
	}
	return tx.Commit()
}

func historicalGoalIn(tx *sql.Tx, session string) (string, error) {
	var goal string
	err := tx.QueryRow(`WITH RECURSIVE ancestry(session,depth,path) AS (
 SELECT ?,0,','||?||',' UNION ALL SELECT l.parent,a.depth+1,a.path||l.parent||',' FROM ym_links l JOIN ancestry a ON l.child=a.session WHERE instr(a.path,','||l.parent||',')=0)
 SELECT m.goal_id FROM ancestry a JOIN ym_manager_sessions s ON s.session=a.session
 JOIN ym_managers m ON 'mgr'||m.num=s.manager WHERE m.goal_id!='' ORDER BY a.depth LIMIT 1`, session, session).Scan(&goal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return goal, err
}

func (s *Store) commitVerdict(g Goal, v Verification, verdict string, findings []string, at time.Time) (Goal, error) {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return g, err
	}
	defer tx.Rollback()
	current, err := goalIn(tx, g.ID)
	if err != nil {
		return g, err
	}
	if current.State != GoalVerifying || current.ActiveVerification != v.ID || current.WorkRevision != v.WorkRevision {
		return g, ErrStale
	}
	scope, err := scopeIn(tx, current)
	if err != nil {
		return g, err
	}
	snapshot, err := prSnapshotIn(tx, g.ID, v)
	if err != nil {
		return g, err
	}
	if scope != v.Scope || snapshot != v.PRSnapshot {
		if err = withdrawAttemptIn(tx, current, v, "effective scope or owned PR snapshot changed; run ym goal done again", at); err != nil {
			return g, err
		}
		if err = tx.Commit(); err != nil {
			return g, err
		}
		return g, ErrStale
	}
	if err = outstandingIn(tx, g.ID); err != nil {
		return g, err
	}
	res, err := tx.Exec(`UPDATE ym_goal_verifications SET verdict=?,findings=?,decided_at=?,fact_checks=?,facts_at=?,version=version+1 WHERE num=? AND version=? AND verifier_session=? AND verdict=''`, verdict, jsonOf(findings), stamp(at), jsonOf(v.Facts), stamp(v.FactsAt), v.ID, v.Version, v.Session)
	if err = changed(res, err); err != nil {
		return g, err
	}
	g = current
	list := "\n  - " + strings.Join(findings, "\n  - ")
	var kind, payload string
	switch verdict {
	case VerdictPass:
		g.State, g.DoneAt, g.Evidence, g.AckedAt = GoalDone, at, "", time.Time{}
		kind, payload = EventVerified, fmt.Sprintf("attempt %d passed:%s", v.Attempt, list)
	case VerdictFail:
		g.State, g.Evidence, g.LastMovedAt = GoalActive, "", at
		kind, payload = EventFailedVerify, fmt.Sprintf("attempt %d failed, so the goal is active again. Fix each finding, then run ym goal done again:%s", v.Attempt, list)
	default:
		g.State, g.Evidence, g.AckedAt = GoalStuck, verifyEvidence+strings.Join(findings, "; "), time.Time{}
		kind, payload = EventStuck, fmt.Sprintf("verification attempt %d could not finish, so the goal shows stuck to the owner. Once the cause is fixed, run ym goal done again:%s", v.Attempt, list)
	}
	_, err = tx.Exec(`UPDATE ym_goals SET state=?,done_at=?,evidence=?,acked_at=?,last_moved_at=?,version=version+1 WHERE num=?`, g.State, stamp(g.DoneAt), g.Evidence, stamp(g.AckedAt), stamp(g.LastMovedAt), strings.TrimPrefix(g.ID, "goal"))
	if err != nil {
		return g, err
	}
	if err = eventIn(tx, g.ID, kind, payload, fmt.Sprintf("completion:%d:verdict", v.ID), at, verdict == VerdictPass); err != nil {
		return g, err
	}
	if verdict == VerdictPass {
		if err = eventIn(tx, g.ID, EventDone, g.Summary, fmt.Sprintf("completion:%d:done", v.ID), at, true); err != nil {
			return g, err
		}
	}
	g.Version++
	return g, tx.Commit()
}

func (s *Store) adoptCompletion(v Verification) (Verification, error) {
	defer s.invalidate()
	if v.Scope != "" {
		return v, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return v, err
	}
	defer tx.Rollback()
	g, err := goalIn(tx, v.Goal)
	if err != nil {
		return v, err
	}
	if g.State != GoalVerifying || g.ActiveVerification != v.ID {
		return v, ErrStale
	}
	if v.Scope, err = scopeIn(tx, g); err != nil {
		return v, err
	}
	if v.PRSnapshot, err = prSnapshotIn(tx, g.ID, v); err != nil {
		return v, err
	}
	v.WorkRevision = g.WorkRevision
	res, err := tx.Exec(`UPDATE ym_goal_verifications SET scope=?,pr_snapshot=?,work_revision=?,version=version+1 WHERE num=? AND version=? AND verdict='' AND scope=''`, v.Scope, v.PRSnapshot, v.WorkRevision, v.ID, v.Version)
	if err = changed(res, err); err != nil {
		return v, err
	}
	v.Version++
	return v, tx.Commit()
}

func withdrawAttemptIn(tx *sql.Tx, g Goal, v Verification, reason string, at time.Time) error {
	res, err := tx.Exec(`UPDATE ym_goal_verifications SET verdict='superseded',findings=?,decided_at=?,version=version+1 WHERE num=? AND version=? AND verifier_session=? AND verdict=''`, jsonOf([]string{reason}), stamp(at), v.ID, v.Version, v.Session)
	if err = changed(res, err); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE ym_goals SET state='active',active_verification=0,evidence='',done_at=NULL,acked_at=NULL,last_moved_at=?,version=version+1 WHERE num=?`, stamp(at), strings.TrimPrefix(g.ID, "goal")); err != nil {
		return err
	}
	return eventIn(tx, g.ID, EventFailedVerify, fmt.Sprintf("attempt %d withdrawn: %s", v.Attempt, reason), fmt.Sprintf("completion:%d:withdrawn", v.ID), at, false)
}

func (s *Store) withdrawCompletion(g Goal, v Verification, reason string, at time.Time) error {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	current, err := goalIn(tx, g.ID)
	if err != nil {
		return err
	}
	if current.State != GoalVerifying || current.ActiveVerification != v.ID || current.WorkRevision != v.WorkRevision {
		return ErrStale
	}
	if err = withdrawAttemptIn(tx, current, v, reason, at); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrStale, reason)
}
