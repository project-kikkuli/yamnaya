package core

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	GoalActive = "active"
	GoalStuck  = "stuck"
	// GoalVerifying is a goal its manager called done that waits on its facts and a verifier's verdict.
	GoalVerifying = "verifying"
	GoalDone      = "done"
	GoalDropped   = "dropped"

	RoleManager = "manager"
	RoleChief   = "chief"

	// Owner is the recipient of an item that climbed past every manager and chief.
	Owner = ""
)

// Goal is one line the owner typed and the manager that owns it until it is done.
type Goal struct {
	ID          string
	Ask         string
	Repo        string
	ManagerID   string
	State       string
	Evidence    string
	Summary     string
	BriefPath   string
	ArtifactURL string
	LastMovedAt time.Time
	CreatedAt   time.Time
	DoneAt      time.Time
	// AckedAt is when the owner cleared the goal's done or stuck row from the queue.
	AckedAt time.Time
	// Host is the host the goal runs on when that is not this one.
	Host string
}

// Manager is a long-lived session's persistent identity: the session it runs as changes when ym relaunches it, the manager does not.
type Manager struct {
	ID   string
	Role string
	Name string
	// ParentID is the chief the manager reports to; empty reports to the owner.
	ParentID    string
	GoalID      string
	Session     string
	Harness     string
	Repo        string
	Directory   string
	BriefPath   string
	ArtifactURL string
	Retired     bool
	LaunchedAt  time.Time
	Failures    int
	NextAttempt time.Time
	LastError   string
	Nudges      int
	NudgedAt    time.Time
	// Host is the host the manager runs on when that is not this one.
	Host string
}

const orgSchema = `
CREATE TABLE IF NOT EXISTS ym_goals (
	num INTEGER PRIMARY KEY AUTOINCREMENT,
	ask TEXT NOT NULL,
	repo TEXT NOT NULL DEFAULT '',
	manager_id TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	evidence TEXT NOT NULL DEFAULT '',
	summary TEXT NOT NULL DEFAULT '',
	brief_path TEXT NOT NULL DEFAULT '',
	artifact_url TEXT NOT NULL DEFAULT '',
	last_moved_at TEXT,
	created_at TEXT NOT NULL,
	done_at TEXT,
	acked_at TEXT
);
CREATE TABLE IF NOT EXISTS ym_managers (
	num INTEGER PRIMARY KEY AUTOINCREMENT,
	role TEXT NOT NULL,
	name TEXT NOT NULL,
	parent_id TEXT NOT NULL DEFAULT '',
	goal_id TEXT NOT NULL DEFAULT '',
	session TEXT NOT NULL DEFAULT '',
	harness TEXT NOT NULL DEFAULT 'claude',
	repo TEXT NOT NULL DEFAULT '',
	directory TEXT NOT NULL DEFAULT '',
	brief_path TEXT NOT NULL DEFAULT '',
	artifact_url TEXT NOT NULL DEFAULT '',
	retired INTEGER NOT NULL DEFAULT 0,
	launched_at TEXT,
	failures INTEGER NOT NULL DEFAULT 0,
	next_attempt TEXT,
	last_error TEXT NOT NULL DEFAULT '',
	nudges INTEGER NOT NULL DEFAULT 0,
	nudged_at TEXT
);
CREATE TABLE IF NOT EXISTS ym_manager_sessions (
	session TEXT PRIMARY KEY,
	manager TEXT NOT NULL,
	at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_repo_authority (
	repo TEXT PRIMARY KEY,
	can_merge INTEGER NOT NULL
);
INSERT OR IGNORE INTO ym_repo_authority (repo, can_merge) VALUES ('auth-plane', 1), ('august', 0);
CREATE TABLE IF NOT EXISTS ym_review_requests (
	url TEXT PRIMARY KEY,
	updated TEXT NOT NULL DEFAULT '',
	state TEXT NOT NULL,
	seen_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_queue_dismissed (
	key TEXT PRIMARY KEY,
	at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_merge_notices (
	url TEXT NOT NULL,
	head TEXT NOT NULL,
	manager TEXT NOT NULL,
	PRIMARY KEY (url, head, manager)
);
`

// itemRecipients adds the column naming who an item waits on: the owner, or a manager's id.
func itemRecipients(db *sql.DB) error {
	var has int
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('ym_items') WHERE name = 'recipient'`).Scan(&has); err != nil || has > 0 {
		return err
	}
	_, err := db.Exec(`ALTER TABLE ym_items ADD COLUMN recipient TEXT NOT NULL DEFAULT ''`)
	return err
}

func goalID(num int64) string    { return "goal" + strconv.FormatInt(num, 10) }
func managerID(num int64) string { return "mgr" + strconv.FormatInt(num, 10) }

func numOf(id, prefix string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(id), prefix), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a %s id: %w", id, prefix, ErrNotFound)
	}
	return n, nil
}

func stamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func (s *Store) AddGoal(g Goal) (Goal, error) {
	if g.CreatedAt.IsZero() {
		g.CreatedAt = time.Now().UTC()
	}
	if g.State == "" {
		g.State = GoalActive
	}
	var num int64
	err := s.db.QueryRow(`INSERT INTO ym_goals (ask, repo, state, brief_path, last_moved_at, created_at) VALUES (?, ?, ?, ?, ?, ?) RETURNING num`,
		g.Ask, g.Repo, g.State, g.BriefPath, stamp(g.CreatedAt), stamp(g.CreatedAt)).Scan(&num)
	g.ID, g.LastMovedAt = goalID(num), g.CreatedAt
	return g, err
}

func (s *Store) SaveGoal(g Goal) error {
	num, err := numOf(g.ID, "goal")
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE ym_goals SET ask=?, repo=?, manager_id=?, state=?, evidence=?, summary=?, brief_path=?, artifact_url=?, last_moved_at=?, done_at=?, acked_at=? WHERE num=?`,
		g.Ask, g.Repo, g.ManagerID, g.State, g.Evidence, g.Summary, g.BriefPath, g.ArtifactURL, stamp(g.LastMovedAt), stamp(g.DoneAt), stamp(g.AckedAt), num)
	return err
}

const goalCols = `num, ask, repo, manager_id, state, evidence, summary, brief_path, artifact_url, last_moved_at, created_at, done_at, acked_at`

func scanGoal(row interface{ Scan(...any) error }) (Goal, error) {
	var g Goal
	var num int64
	var moved, created, done, acked sql.NullString
	err := row.Scan(&num, &g.Ask, &g.Repo, &g.ManagerID, &g.State, &g.Evidence, &g.Summary, &g.BriefPath, &g.ArtifactURL, &moved, &created, &done, &acked)
	g.ID = goalID(num)
	g.LastMovedAt, g.CreatedAt, g.DoneAt, g.AckedAt = parseTime(moved), parseTime(created), parseTime(done), parseTime(acked)
	return g, err
}

func (s *Store) Goal(id string) (Goal, error) {
	num, err := numOf(id, "goal")
	if err != nil {
		return Goal{}, err
	}
	g, err := scanGoal(s.db.QueryRow(`SELECT `+goalCols+` FROM ym_goals WHERE num = ?`, num))
	if errors.Is(err, sql.ErrNoRows) {
		return g, fmt.Errorf("goal %s: %w", id, ErrNotFound)
	}
	return g, err
}

func (s *Store) Goals() ([]Goal, error) {
	return remembered(s, "goals", cloneSlice[Goal], s.goals)
}

func (s *Store) goals() ([]Goal, error) {
	rows, err := s.db.Query(`SELECT ` + goalCols + ` FROM ym_goals ORDER BY num`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Goal
	for rows.Next() {
		g, err := scanGoal(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) AddManager(m Manager) (Manager, error) {
	if m.Harness == "" {
		m.Harness = "claude"
	}
	var num int64
	err := s.db.QueryRow(`INSERT INTO ym_managers (role, name, parent_id, goal_id, harness, repo, brief_path, launched_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING num`,
		m.Role, m.Name, m.ParentID, m.GoalID, m.Harness, m.Repo, m.BriefPath, stamp(m.LaunchedAt)).Scan(&num)
	m.ID = managerID(num)
	return m, err
}

func (s *Store) SaveManager(m Manager) error {
	num, err := numOf(m.ID, "mgr")
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE ym_managers SET role=?, name=?, parent_id=?, goal_id=?, session=?, harness=?, repo=?, directory=?, brief_path=?, artifact_url=?,
		retired=?, launched_at=?, failures=?, next_attempt=?, last_error=?, nudges=?, nudged_at=? WHERE num=?`,
		m.Role, m.Name, m.ParentID, m.GoalID, m.Session, m.Harness, m.Repo, m.Directory, m.BriefPath, m.ArtifactURL,
		m.Retired, stamp(m.LaunchedAt), m.Failures, stamp(m.NextAttempt), m.LastError, m.Nudges, stamp(m.NudgedAt), num); err != nil {
		return err
	}
	if m.Session != "" {
		at := now()
		if !m.LaunchedAt.IsZero() {
			at = m.LaunchedAt.UTC().Format(time.RFC3339Nano)
		}
		if _, err = tx.Exec(`INSERT OR IGNORE INTO ym_manager_sessions (session, manager, at) VALUES (?, ?, ?)`, m.Session, m.ID, at); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const managerCols = `num, role, name, parent_id, goal_id, session, harness, repo, directory, brief_path, artifact_url, retired, launched_at, failures, next_attempt, last_error, nudges, nudged_at`

func scanManager(row interface{ Scan(...any) error }) (Manager, error) {
	var m Manager
	var num int64
	var launched, next, nudged sql.NullString
	err := row.Scan(&num, &m.Role, &m.Name, &m.ParentID, &m.GoalID, &m.Session, &m.Harness, &m.Repo, &m.Directory, &m.BriefPath, &m.ArtifactURL,
		&m.Retired, &launched, &m.Failures, &next, &m.LastError, &m.Nudges, &nudged)
	m.ID = managerID(num)
	m.LaunchedAt, m.NextAttempt, m.NudgedAt = parseTime(launched), parseTime(next), parseTime(nudged)
	return m, err
}

func (s *Store) Manager(id string) (Manager, error) {
	num, err := numOf(id, "mgr")
	if err != nil {
		return Manager{}, err
	}
	m, err := scanManager(s.db.QueryRow(`SELECT `+managerCols+` FROM ym_managers WHERE num = ?`, num))
	if errors.Is(err, sql.ErrNoRows) {
		return m, fmt.Errorf("manager %s: %w", id, ErrNotFound)
	}
	return m, err
}

// Managers lists every manager and chief, retired ones too, oldest first.
func (s *Store) Managers() ([]Manager, error) {
	return remembered(s, "managers", cloneSlice[Manager], s.managers)
}

func (s *Store) managers() ([]Manager, error) {
	rows, err := s.db.Query(`SELECT ` + managerCols + ` FROM ym_managers ORDER BY num`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Manager
	for rows.Next() {
		m, err := scanManager(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ManagerSessions maps every session a manager has ever run as to that manager.
func (s *Store) ManagerSessions() (map[string]string, error) {
	return remembered(s, "manager sessions", cloneMap[string, string], s.managerSessions)
}

func (s *Store) managerSessions() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT session, manager FROM ym_manager_sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var session, manager string
		if err := rows.Scan(&session, &manager); err != nil {
			return nil, err
		}
		out[session] = manager
	}
	return out, rows.Err()
}

// CanMerge reports whether managers may merge their own pull requests in the repository named repo.
func (s *Store) CanMerge(repo string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(`SELECT can_merge FROM ym_repo_authority WHERE repo = ?`, strings.ToLower(repo)).Scan(&ok)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return ok, err
}

func (s *Store) SetAuthority(repo string, canMerge bool) error {
	_, err := s.db.Exec(`INSERT INTO ym_repo_authority (repo, can_merge) VALUES (?, ?) ON CONFLICT(repo) DO UPDATE SET can_merge = excluded.can_merge`, strings.ToLower(repo), canMerge)
	return err
}

func (s *Store) Authority() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT repo, can_merge FROM ym_repo_authority ORDER BY repo`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var repo string
		var ok bool
		if err := rows.Scan(&repo, &ok); err != nil {
			return nil, err
		}
		out[repo] = ok
	}
	return out, rows.Err()
}

// ReviewRequest is a pull request GitHub says waits on the owner's own review.
type ReviewRequest struct {
	URL     string
	Updated string
}

// SyncReviews makes the open review requests exactly reqs: new ones open, ones GitHub no longer lists close.
func (s *Store) SyncReviews(reqs []ReviewRequest) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`UPDATE ym_review_requests SET state = 'gone' WHERE state = 'open'`); err != nil {
		return err
	}
	for _, r := range reqs {
		if _, err = tx.Exec(`INSERT INTO ym_review_requests (url, updated, state, seen_at) VALUES (?, ?, 'open', ?)
			ON CONFLICT(url) DO UPDATE SET state = 'open', updated = excluded.updated, seen_at = excluded.seen_at`, r.URL, r.Updated, now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) OpenReviews() ([]ReviewRequest, error) {
	return remembered(s, "reviews", cloneSlice[ReviewRequest], s.openReviews)
}

func (s *Store) openReviews() ([]ReviewRequest, error) {
	rows, err := s.db.Query(`SELECT url, updated FROM ym_review_requests WHERE state = 'open' ORDER BY seen_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReviewRequest
	for rows.Next() {
		var r ReviewRequest
		if err := rows.Scan(&r.URL, &r.Updated); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DismissQueue hides the queue rows keys name until their keys change, as a new PR head or review update does.
func (s *Store) DismissQueue(keys ...string) error {
	for _, key := range keys {
		if _, err := s.db.Exec(`INSERT OR IGNORE INTO ym_queue_dismissed (key, at) VALUES (?, ?)`, key, now()); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueDismissed() (map[string]bool, error) {
	return remembered(s, "dismissed", cloneMap[string, bool], s.readDismissed)
}

func (s *Store) readDismissed() (map[string]bool, error) {
	rows, err := s.db.Query(`SELECT key FROM ym_queue_dismissed`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// NoteMerge records that manager was told the PR at head may be merged; false when it already was.
func (s *Store) NoteMerge(url, head, manager string) (bool, error) {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO ym_merge_notices (url, head, manager) VALUES (?, ?, ?)`, url, head, manager)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// SetRecipient hands an item to who: a manager's id, or Owner.
func (s *Store) SetRecipient(seq int64, who string) error {
	_, err := s.db.Exec(`UPDATE ym_items SET recipient = ? WHERE seq = ?`, who, seq)
	return err
}
