package cli

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/host/tmux"
)

// fakeCodex puts a codex on PATH, and a Codex home whose thread table the test writes as Codex would; each start writes its
// directory to the file it returns, emptied by the caller before the next start.
func fakeCodex(t *testing.T) (*sql.DB, string) {
	t.Helper()
	bins, home, ran := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "ran")
	if err := os.WriteFile(filepath.Join(bins, "codex"), []byte("#!/bin/sh\necho \"$PWD\" > "+ran+"\nexec sleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bins+":"+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", home)
	t.Setenv("YM_CODEX_FIXTURE_ID_FILE", ran+".thread")
	t.Setenv("YAMNAYA_STATE_DIR", t.TempDir())
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, created_at INTEGER, updated_at INTEGER, cwd TEXT,
		title TEXT, archived INTEGER DEFAULT 0, first_user_message TEXT DEFAULT '', name TEXT, git_branch TEXT, agent_role TEXT,
		agent_nickname TEXT, agent_path TEXT, model TEXT)`,
		`CREATE TABLE thread_spawn_edges (parent_thread_id TEXT NOT NULL, child_thread_id TEXT NOT NULL PRIMARY KEY, status TEXT NOT NULL)`} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db, ran
}

// recordThread writes thread id into Codex's table once the fake codex has started, as Codex does a moment after it starts.
func recordThread(t *testing.T, db *sql.DB, ran, id string) {
	t.Helper()
	if err := os.WriteFile(ran+".thread", []byte(id), 0600); err != nil {
		t.Fatal(err)
	}
	go func() {
		for end := time.Now().Add(10 * time.Second); time.Now().Before(end); time.Sleep(100 * time.Millisecond) {
			if b, err := os.ReadFile(ran); err == nil && len(b) > 0 {
				time.Sleep(500 * time.Millisecond)
				db.Exec(`INSERT INTO threads (id, rollout_path, created_at, updated_at, cwd) VALUES (?, '', ?, ?, ?)`, id, time.Now().Unix(), time.Now().Unix(),
					strings.TrimSpace(string(b)))
				return
			}
		}
	}()
}

func TestACodexManagerIsKeyedByTheThreadCodexRecordsOnStartAndOnStartingOver(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	db, ran := fakeCodex(t)
	base, _ := filepath.EvalSymlinks(t.TempDir())
	repo := filepath.Join(base, "repo")
	git(t, base, "init", "-q", "--bare", "-b", "main", "origin.git")
	git(t, base, "clone", "-q", filepath.Join(base, "origin.git"), "repo")
	git(t, repo, "commit", "-q", "--allow-empty", "-m", "root")
	git(t, repo, "push", "-q", "origin", "main")
	l := testLauncher(t, base)

	recordThread(t, db, ran, "019a0000-0000-7000-8000-000000000001")
	st, err := l.Start(core.NewLaunch{Prompt: "own the goal", Harness: "codex", Repo: repo, Slug: "mgr1-own-goal", Session: "ignored", Await: true})
	if err != nil {
		t.Fatal(err)
	}
	if st.Session != "019a0000-0000-7000-8000-000000000001" || !strings.Contains(st.Message, core.Short(st.Session)) {
		t.Fatalf("started %+v, want the thread codex recorded", st)
	}

	if _, err := db.Exec(`UPDATE threads SET updated_at = ?`, time.Now().Add(-time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	if err := l.srv.Kill(tmux.SessionName(st.Key)); err != nil {
		t.Fatal(err)
	}
	os.Remove(ran)
	recordThread(t, db, ran, "019a0000-0000-7000-8000-000000000002")
	now, resumed, err := l.Resume(st.Session, "you stopped", "own the goal again")
	if err != nil {
		t.Fatal(err)
	}
	if resumed || now != "019a0000-0000-7000-8000-000000000002" {
		t.Fatalf("resume of a thread with no rollout gave %s resumed %v, want a fresh start under the new thread", now, resumed)
	}
	if rec, _ := l.records.Find(st.Key); rec.Session != now {
		t.Fatalf("launch record keeps %s, want %s", rec.Session, now)
	}
	actions, err := l.core.Store.Activations()
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 1 || actions[0].Session != now || actions[0].Kind != "directive" || !strings.Contains(actions[0].Payload, "you stopped") {
		t.Fatalf("recovery lost its new native identity or persisted work: %+v", actions)
	}
}
