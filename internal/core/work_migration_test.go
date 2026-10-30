package core

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func TestLegacyCompletionHistoryMigratesAndActiveClaimCanBeWithdrawn(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(schema + orgSchema + verifySchema + eventSchema); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO ym_goals (ask,state,created_at) VALUES ('research','done','2026-10-01T00:00:00Z'),('build','verifying','2026-10-01T00:00:00Z');
 INSERT INTO ym_goal_verifications (goal,attempt,summary,verdict,created_at) VALUES ('goal1',1,'research complete','pass','2026-10-01T00:00:00Z'),('goal2',1,'build complete','','2026-10-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	history, err := s.Verifications("goal1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].Verdict != VerdictPass || history[0].Summary != "research complete" || history[0].Scope != "" {
		t.Fatalf("history changed during migration: %+v", history)
	}
	g, err := s.Goal("goal2")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.OpenVerification(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g.ActiveVerification != v.ID || g.WorkRevision != 0 {
		t.Fatalf("legacy current attempt not bound: %+v %+v", g, v)
	}
	v, err = s.adoptCompletion(v)
	if err != nil {
		t.Fatal(err)
	}
	if v.Scope == "" || v.PRSnapshot == "" {
		t.Fatalf("legacy claim lacks snapshot: %+v", v)
	}
	if err = s.ResumeGoal(g.ID, "migration-work", "authorized follow-on", v.CreatedAt); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.ResumeGoal(g.ID, "migration-work", "authorized follow-on", v.CreatedAt); err != nil {
		t.Fatal(err)
	}
	g, err = s.Goal(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := s.Verifications(g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g.State != GoalActive || g.WorkRevision != 1 || attempts[0].Verdict != VerdictSuperseded {
		t.Fatalf("restart restored obsolete claim: %+v %+v", g, attempts)
	}
}
