package codex

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestConversationTellsAGoneThreadFromOneItCannotRead(t *testing.T) {
	const id = "01a1b2c3-0000-7000-8000-000000000042"
	meta := `{"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n"
	for _, c := range []struct {
		name  string
		setup func(*testing.T, string)
		found bool
		fails bool
	}{
		{name: "no rollout anywhere", setup: func(*testing.T, string) {}},
		{name: "home missing", fails: true, setup: func(t *testing.T, home string) {
			if err := os.RemoveAll(home); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "rollout found by its id", found: true, setup: func(t *testing.T, home string) {
			put(t, filepath.Join(home, "sessions", "rollout-2026-01-01T00-00-00-"+id+".jsonl"), meta)
		}},
		{name: "state database with no threads table", fails: true, setup: func(t *testing.T, home string) {
			state(t, home, ``)
		}},
		{name: "row names a rollout since archived", found: true, setup: func(t *testing.T, home string) {
			state(t, home, filepath.Join(home, "sessions", "gone.jsonl"))
			put(t, filepath.Join(home, "archived_sessions", "rollout-2026-01-01T00-00-00-"+id+".jsonl"), meta)
		}},
		{name: "row names a rollout gone everywhere", setup: func(t *testing.T, home string) {
			state(t, home, filepath.Join(home, "sessions", "gone.jsonl"))
		}},
		{name: "state home unreadable", fails: true, setup: func(t *testing.T, home string) {
			if err := os.Mkdir(home+"-state", 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(home+"-state", 0700) })
		}},
		{name: "home unreadable", fails: true, setup: func(t *testing.T, home string) {
			if err := os.Chmod(home, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(home, 0700) })
		}},
		{name: "rollout unreadable", fails: true, setup: func(t *testing.T, home string) {
			p := filepath.Join(home, "sessions", "rollout-2026-01-01T00-00-00-"+id+".jsonl")
			put(t, p, meta)
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { os.Chmod(p, 0600) })
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "codex")
			if err := os.MkdirAll(home, 0700); err != nil {
				t.Fatal(err)
			}
			c.setup(t, home)
			stateHome := home
			if _, err := os.Stat(home + "-state"); err == nil {
				stateHome = home + "-state"
			}
			a := &Adapter{Home: home, StateHome: stateHome}
			path, err := a.Conversation(id)
			if (err != nil) != c.fails || (path != "") != c.found {
				t.Fatalf("path=%q err=%v, want found=%v fails=%v", path, err, c.found, c.fails)
			}
		})
	}
}

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

// state makes a Codex state database; an empty rollout leaves out the threads table.
func state(t *testing.T, home, rollout string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE other(x)`); err != nil {
		t.Fatal(err)
	}
	if rollout == "" {
		return
	}
	if _, err = db.Exec(`CREATE TABLE threads(id TEXT, rollout_path TEXT)`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO threads VALUES(?, ?)`, "01a1b2c3-0000-7000-8000-000000000042", rollout); err != nil {
		t.Fatal(err)
	}
}
