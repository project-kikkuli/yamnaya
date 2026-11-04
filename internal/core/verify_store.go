package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const verifySchema = `
CREATE TABLE IF NOT EXISTS ym_goal_verifications (
	num INTEGER PRIMARY KEY AUTOINCREMENT,
	goal TEXT NOT NULL,
	attempt INTEGER NOT NULL,
	verifier_session TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	evidence TEXT NOT NULL DEFAULT '[]',
	fact_checks TEXT NOT NULL DEFAULT '[]',
	facts_at TEXT,
	verdict TEXT NOT NULL DEFAULT '',
	findings TEXT NOT NULL DEFAULT '[]',
	launched_at TEXT,
	failures INTEGER NOT NULL DEFAULT 0,
	next_attempt TEXT,
	nudged_at TEXT,
	last_error TEXT NOT NULL DEFAULT '',
	ended INTEGER NOT NULL DEFAULT 0,
	created_at TEXT NOT NULL,
	decided_at TEXT
);
CREATE INDEX IF NOT EXISTS ym_goal_verifications_goal ON ym_goal_verifications (goal, attempt);
CREATE TABLE IF NOT EXISTS ym_verifier_sessions (
	session TEXT PRIMARY KEY,
	verification INTEGER NOT NULL,
	at TEXT NOT NULL
);
INSERT OR IGNORE INTO ym_verifier_sessions (session, verification, at)
	SELECT verifier_session, num, coalesce(launched_at, created_at) FROM ym_goal_verifications
	WHERE verifier_session != '' ORDER BY num;
`

type VerifierSession struct {
	Verification int64
	Goal         string
}

func (s *Store) VerifierSessions() (map[string]VerifierSession, error) {
	return remembered(s, "verifier sessions", cloneMap[string, VerifierSession], s.verifierSessions)
}

func (s *Store) verifierSessions() (map[string]VerifierSession, error) {
	rows, err := s.db.Query(`SELECT s.session, s.verification, v.goal FROM ym_verifier_sessions s
		JOIN ym_goal_verifications v ON v.num = s.verification`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]VerifierSession{}
	for rows.Next() {
		var session string
		var v VerifierSession
		if err := rows.Scan(&session, &v.Verification, &v.Goal); err != nil {
			return nil, err
		}
		out[session] = v
	}
	return out, rows.Err()
}

func bindVerifier(tx *sql.Tx, id int64) error {
	_, err := tx.Exec(`INSERT OR IGNORE INTO ym_verifier_sessions (session, verification, at)
		SELECT verifier_session, num, coalesce(launched_at, created_at) FROM ym_goal_verifications
		WHERE num = ? AND verifier_session != ''`, id)
	return err
}

// Verification is one attempt at proving a goal done: the manager's claim, the deterministic facts ym checked, and the verdict of
// a fresh session, on the manager's harness, that is neither the manager nor one of its agents.
type Verification struct {
	checkedCache        string
	previousSession     string
	recovery            bool
	expectedGoalVersion int64
	Version             int64
	WorkRevision        int64
	Scope               string
	PRSnapshot          string
	ID                  int64
	Goal                string
	Attempt             int
	// Session is the verifier's current session; a relaunch replaces it with a fresh one.
	Session string
	Summary string
	// Evidence is what the manager claimed: its evidence lines, then the pull requests of the goal.
	Evidence []string
	Facts    []Fact
	FactsAt  time.Time
	Verdict  string
	Findings []string
	// LaunchedAt, Failures and NextAttempt supervise the verifier session the way a manager's session is supervised.
	LaunchedAt  time.Time
	Failures    int
	NextAttempt time.Time
	NudgedAt    time.Time
	LastError   string
	// Ended is set once ym has ended the verifier session of a decided verification.
	Ended     bool
	CreatedAt time.Time
	DecidedAt time.Time
}

// Fact is one deterministic check on a pull request the goal claims: merged, CI green on the merge commit, no open threads.
type Fact struct {
	Snapshot string `json:"snapshot,omitempty"`
	Revision string `json:"revision,omitempty"`
	Check    string `json:"check"`
	Subject  string `json:"subject"`
	OK       bool   `json:"ok"`
	Pending  bool   `json:"pending,omitempty"`
	Detail   string `json:"detail"`
}

func (f Fact) String() string {
	word := "fail"
	switch {
	case f.Pending:
		word = "pending"
	case f.OK:
		word = "ok"
	}
	return fmt.Sprintf("%s %s %s: %s", word, f.Check, f.Subject, f.Detail)
}

const verificationCols = `num, goal, attempt, verifier_session, summary, evidence, fact_checks, facts_at, verdict, findings, launched_at, failures,
	next_attempt, nudged_at, last_error, ended, created_at, decided_at, version, work_revision, scope, pr_snapshot`

func scanVerification(row interface{ Scan(...any) error }) (Verification, error) {
	var v Verification
	var evidence, facts, findings string
	var factsAt, launched, next, nudged, created, decided sql.NullString
	err := row.Scan(&v.ID, &v.Goal, &v.Attempt, &v.Session, &v.Summary, &evidence, &facts, &factsAt, &v.Verdict, &findings, &launched,
		&v.Failures, &next, &nudged, &v.LastError, &v.Ended, &created, &decided, &v.Version, &v.WorkRevision, &v.Scope, &v.PRSnapshot)
	if err != nil {
		return v, err
	}
	v.FactsAt, v.LaunchedAt, v.NextAttempt, v.NudgedAt = parseTime(factsAt), parseTime(launched), parseTime(next), parseTime(nudged)
	v.CreatedAt, v.DecidedAt = parseTime(created), parseTime(decided)
	err = errors.Join(json.Unmarshal([]byte(evidence), &v.Evidence), json.Unmarshal([]byte(facts), &v.Facts), json.Unmarshal([]byte(findings), &v.Findings))
	return v, err
}

func jsonOf(v any) string {
	raw, _ := json.Marshal(v)
	if string(raw) == "null" {
		return "[]"
	}
	return string(raw)
}

// AddVerification opens the next attempt for v.Goal.
func (s *Store) AddVerification(v Verification) (Verification, error) {
	defer s.invalidate()
	if v.CreatedAt.IsZero() {
		v.CreatedAt = time.Now().UTC()
	}
	g, err := s.Goal(v.Goal)
	if err != nil {
		return v, err
	}
	v.WorkRevision = g.WorkRevision
	_, v, err = s.openCompletion(v)
	return v, err
}

func (s *Store) SaveVerification(v Verification) error {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = bindVerifier(tx, v.ID); err != nil {
		return err
	}
	if v.Verdict != "" {
		res, err := tx.Exec(`UPDATE ym_goal_verifications SET ended=?,version=version+1 WHERE num=? AND version=? AND verdict=?`, v.Ended, v.ID, v.Version, v.Verdict)
		if err = changed(res, err); err != nil {
			return err
		}
		return tx.Commit()
	}

	res, err := tx.Exec(`UPDATE ym_goal_verifications SET version=version+1, verifier_session=?, fact_checks=?, facts_at=?,
        launched_at=?, failures=?, next_attempt=?, nudged_at=?, last_error=?, ended=?
        WHERE num=? AND version=? AND verdict=? AND (verdict!='' OR EXISTS
        (SELECT 1 FROM ym_goals g WHERE 'goal'||g.num=goal AND g.state='verifying' AND g.active_verification=ym_goal_verifications.num AND g.work_revision=ym_goal_verifications.work_revision))`,
		v.Session, jsonOf(v.Facts), stamp(v.FactsAt), stamp(v.LaunchedAt), v.Failures, stamp(v.NextAttempt), stamp(v.NudgedAt), v.LastError, v.Ended,
		v.ID, v.Version, v.Verdict)
	if err = changed(res, err); err != nil {
		return err
	}
	if err = bindVerifier(tx, v.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// Verifications lists goal's attempts, oldest first; an empty goal lists every goal's.
func (s *Store) Verifications(goal string) ([]Verification, error) {
	rows, err := s.db.Query(`SELECT `+verificationCols+` FROM ym_goal_verifications WHERE ? = '' OR goal = ? ORDER BY num`, goal, goal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Verification
	for rows.Next() {
		v, err := scanVerification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// verifierGoal is the goal the session, by either id, is verifying now.
func (s *Store) verifierGoal(session, resolved string) (string, error) {
	var goal string
	err := s.db.QueryRow(`SELECT goal FROM ym_goal_verifications WHERE verdict = '' AND verifier_session IN (?, ?) ORDER BY num DESC LIMIT 1`, session, resolved).Scan(&goal)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("session %s is verifying no goal: %w", Short(session), ErrNotFound)
	}
	return goal, err
}

// OpenVerification is goal's undecided attempt.
func (s *Store) OpenVerification(goal string) (Verification, error) {
	v, err := scanVerification(s.db.QueryRow(`SELECT `+verificationCols+` FROM ym_goal_verifications WHERE goal = ? AND verdict = '' ORDER BY num DESC LIMIT 1`, goal))
	if errors.Is(err, sql.ErrNoRows) {
		if g, err := s.Goal(goal); err == nil {
			goal = g.Shown()
		}
		return v, fmt.Errorf("goal %s has no verification under way: %w", goal, ErrNotFound)
	}
	return v, err
}
