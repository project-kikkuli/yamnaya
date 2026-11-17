package core

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

type LinearComment struct {
	ID, Body, URL, Author, CreatedAt, UpdatedAt string
}

type LinearRelation struct {
	ID, Type, IssueID, Identifier, Title, URL string
	Inverse                                   bool
}

type LinearIssue struct {
	ID, Identifier, Title, Description, URL, UpdatedAt    string
	State, StateType, Team, Project, AssigneeID, Assignee string
	Priority                                              int
	Assigned, Unavailable, Watched                        bool
	Comments                                              []LinearComment
	Relations                                             []LinearRelation
	Goal, Attention                                       string
	AttentionVersion                                      int
	ReadAt, HydratedAt                                    time.Time
}

func (i LinearIssue) Active() bool {
	return i.StateType != "completed" && i.StateType != "canceled" && i.StateType != "duplicate"
}

type LinearSnapshot struct {
	OwnerID, OwnerName, WorkspaceID, Workspace string
	Issues                                     []LinearIssue
	At                                         time.Time
	Error                                      string
}

const linearSchema = `
CREATE TABLE IF NOT EXISTS ym_linear_issues (
 id TEXT PRIMARY KEY, data TEXT NOT NULL, assigned INTEGER NOT NULL DEFAULT 0,
 goal TEXT NOT NULL DEFAULT '', attention TEXT NOT NULL DEFAULT '',
 attention_version INTEGER NOT NULL DEFAULT 0, read_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS ym_linear_source (
 id INTEGER PRIMARY KEY CHECK(id=1), owner_id TEXT NOT NULL DEFAULT '', owner_name TEXT NOT NULL DEFAULT '',
 workspace_id TEXT NOT NULL DEFAULT '', workspace TEXT NOT NULL DEFAULT '',
 at TEXT, error TEXT NOT NULL DEFAULT ''
);
`

func (s *Store) LinearSnapshot() (LinearSnapshot, error) {
	var out LinearSnapshot
	var at sql.NullString
	err := s.db.QueryRow(`SELECT owner_id,owner_name,workspace_id,workspace,at,error FROM ym_linear_source WHERE id=1`).Scan(&out.OwnerID, &out.OwnerName, &out.WorkspaceID, &out.Workspace, &at, &out.Error)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.At = parseTime(at)
	rows, err := s.db.Query(`SELECT data,assigned,goal,attention,attention_version,read_at FROM ym_linear_issues ORDER BY id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var i LinearIssue
		var raw string
		var read sql.NullString
		if err = rows.Scan(&raw, &i.Assigned, &i.Goal, &i.Attention, &i.AttentionVersion, &read); err != nil {
			return out, err
		}
		goal, attention, version, assigned := i.Goal, i.Attention, i.AttentionVersion, i.Assigned
		if err = json.Unmarshal([]byte(raw), &i); err != nil {
			return out, err
		}
		i.Goal, i.Attention, i.AttentionVersion, i.Assigned, i.ReadAt = goal, attention, version, assigned, parseTime(read)
		out.Issues = append(out.Issues, i)
	}
	return out, rows.Err()
}

func (s *Store) SyncLinear(snapshot LinearSnapshot) error {
	if snapshot.OwnerID == "" || snapshot.WorkspaceID == "" {
		return errors.New("Linear snapshot has no authenticated owner or workspace")
	}
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner, workspace string
	err = tx.QueryRow(`SELECT owner_id,workspace_id FROM ym_linear_source WHERE id=1`).Scan(&owner, &workspace)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if owner != "" && (owner != snapshot.OwnerID || workspace != snapshot.WorkspaceID) {
		return errors.New("Linear account changed; disconnect and explicitly clear the previous source before replacing it")
	}
	for _, i := range snapshot.Issues {
		if i.ID == "" || i.Identifier == "" {
			return errors.New("Linear snapshot includes an issue without identity")
		}
		i.Goal, i.Attention, i.AttentionVersion = "", "", 0
		raw, err := json.Marshal(i)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO ym_linear_issues(id,data,assigned,read_at) VALUES(?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data,assigned=excluded.assigned,read_at=excluded.read_at`, i.ID, string(raw), i.Assigned, stamp(linearReadAt(i, snapshot.At))); err != nil {
			return err
		}
	}
	_, err = tx.Exec(`INSERT INTO ym_linear_source(id,owner_id,owner_name,workspace_id,workspace,at,error) VALUES(1,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET owner_id=excluded.owner_id,owner_name=excluded.owner_name,workspace_id=excluded.workspace_id,workspace=excluded.workspace,at=excluded.at,error=excluded.error`, snapshot.OwnerID, snapshot.OwnerName, snapshot.WorkspaceID, snapshot.Workspace, stamp(snapshot.At), snapshot.Error)
	if err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) LinearFailure(err error) error {
	defer s.invalidate()
	message := ""
	if err != nil {
		message = err.Error()
	}
	_, e := s.db.Exec(`INSERT INTO ym_linear_source(id,error) VALUES(1,?) ON CONFLICT(id) DO UPDATE SET error=excluded.error`, message)
	return e
}

func (s *Store) LinearIssue(ref string) (LinearIssue, error) {
	snap, err := s.LinearSnapshot()
	if err != nil {
		return LinearIssue{}, err
	}
	for _, i := range snap.Issues {
		if i.ID == ref || strings.EqualFold(i.Identifier, ref) || i.URL == ref {
			return i, nil
		}
	}
	return LinearIssue{}, fmt.Errorf("Linear issue %q is not cached; run ym linear sync or watch it first", ref)
}

func (s *Store) LinkLinear(ref, goal string) error {
	i, err := s.LinearIssue(ref)
	if err != nil {
		return err
	}
	if goal != "" {
		if _, err = s.Goal(goal); err != nil {
			return err
		}
	}
	defer s.invalidate()
	_, err = s.db.Exec(`UPDATE ym_linear_issues SET goal=? WHERE id=?`, goal, i.ID)
	return err
}

func (s *Store) LinearAttention(ref, reason string) error {
	i, err := s.LinearIssue(ref)
	if err != nil {
		return err
	}
	defer s.invalidate()
	_, err = s.db.Exec(`UPDATE ym_linear_issues SET attention_version=attention_version+CASE WHEN attention<>? THEN 1 ELSE 0 END,attention=? WHERE id=?`, reason, reason, i.ID)
	return err
}

func (s *Store) ClearLinear() error {
	defer s.invalidate()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM ym_linear_issues`); err != nil {
		return err
	}
	if _, err = tx.Exec(`DELETE FROM ym_linear_source`); err != nil {
		return err
	}
	return tx.Commit()
}

func (b *Board) attachLinear(snapshot LinearSnapshot) {
	byID := map[string]LinearIssue{}
	for _, remote := range b.Remotes {
		for _, i := range remote.Linear.Issues {
			if old, ok := byID[i.ID]; !ok || i.ReadAt.After(old.ReadAt) {
				byID[i.ID] = i
			}
		}
	}
	for _, i := range snapshot.Issues {
		if old, ok := byID[i.ID]; ok && old.ReadAt.After(i.ReadAt) {
			old.Goal, old.Attention, old.AttentionVersion, old.Watched = i.Goal, i.Attention, i.AttentionVersion, i.Watched
			byID[i.ID] = old
		} else {
			byID[i.ID] = i
		}
	}
	b.Linear = snapshot
	b.Linear.Issues = nil
	for _, i := range byID {
		b.Linear.Issues = append(b.Linear.Issues, i)
	}
	sort.Slice(b.Linear.Issues, func(i, j int) bool { return b.Linear.Issues[i].Identifier < b.Linear.Issues[j].Identifier })
}

func (c *Core) linearNeeds(b *Board, dismissed map[string]bool) error {
	snap, err := c.Store.LinearSnapshot()
	if err != nil {
		return err
	}
	b.attachLinear(snap)
	for _, issue := range b.Linear.Issues {
		if issue.Attention == "" {
			continue
		}
		i := issue
		n := Need{Linear: &i}
		if !dismissed[n.Key()] {
			b.Needs = append(b.Needs, n)
		}
	}
	return nil
}

func linearReadAt(i LinearIssue, at time.Time) time.Time {
	if !i.ReadAt.IsZero() {
		return i.ReadAt
	}
	return at
}

func (i LinearIssue) Details() []string {
	out := []string{fmt.Sprintf("%s [%s] P%d %s", i.Identifier, i.State, i.Priority, i.Title)}
	if i.Goal != "" {
		out = append(out, "stream: "+i.Goal)
	}
	if i.Unavailable {
		out = append(out, "unavailable at last check; showing cached evidence")
	}
	for _, r := range i.Relations {
		direction := ""
		if r.Inverse {
			direction = " (incoming)"
		}
		out = append(out, r.Type+direction+": "+r.Identifier+" "+r.Title)
	}
	return append(out, i.URL)
}
