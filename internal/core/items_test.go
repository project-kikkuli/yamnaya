package core

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

func TestItemIDsNumberPerScopeAcrossKinds(t *testing.T) {
	st := newWorld(t).core.Store
	add := func(scope, kind string) Item {
		t.Helper()
		it, err := st.AddItem(Item{Scope: scope, Kind: kind, Question: "q", Session: "s"})
		if err != nil {
			t.Fatal(err)
		}
		return it
	}
	cases := []struct {
		scope, kind, want string
	}{
		{"/src/app/.git", KindDecision, "D1"},
		{"/src/app/.git", KindChore, "C2"},
		{"/src/lib.git", KindDecision, "D1"},
		{"/src/app/.git", KindDefault, "V3"},
		{"/home/notes", KindDecision, "D1"},
	}
	for _, c := range cases {
		if got := add(c.scope, c.kind); got.ID != c.want {
			t.Errorf("%s %s = %s, want %s", c.scope, c.kind, got.ID, c.want)
		}
	}
	resolve := []struct {
		scope, ref, wantScope string
		ok                    bool
	}{
		{"/src/lib.git", "d1", "/src/lib.git", true},
		{"/src/app/.git", "D1", "/src/app/.git", true},
		{"", "lib:D1", "/src/lib.git", true},
		{"", "notes:D1", "/home/notes", true},
		{"", "C2", "/src/app/.git", true},
		{"", "D1", "", false},
		{"/src/lib.git", "C9", "", false},
	}
	for _, r := range resolve {
		it, err := st.Find(r.scope, r.ref)
		if (err == nil) != r.ok || (r.ok && it.Scope != r.wantScope) {
			t.Errorf("Find(%q, %q) = %s in %q, %v", r.scope, r.ref, it.ID, it.Scope, err)
		}
	}
}

func TestOldGlobalItemsMigrateToPerScopeIDsKeepingAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ym.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE ym_items (seq INTEGER PRIMARY KEY AUTOINCREMENT, id TEXT NOT NULL UNIQUE, kind TEXT NOT NULL, question TEXT NOT NULL,
			rec TEXT NOT NULL DEFAULT '', options TEXT NOT NULL DEFAULT '[]', assumption TEXT NOT NULL DEFAULT '', blocks INTEGER NOT NULL DEFAULT 0,
			session TEXT NOT NULL, harness TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, state TEXT NOT NULL, answer TEXT NOT NULL DEFAULT '',
			reason TEXT NOT NULL DEFAULT '', answered_at TEXT, delivered_at TEXT)`,
		`CREATE TABLE ym_item_events (seq INTEGER PRIMARY KEY AUTOINCREMENT, item TEXT NOT NULL, at TEXT NOT NULL, from_state TEXT NOT NULL,
			to_state TEXT NOT NULL, text TEXT NOT NULL DEFAULT '')`,
		`INSERT INTO ym_items (id, kind, question, session, created_at, state) VALUES
			('D1', 'decision', 'app one', 'app-lead', '2026-10-01T00:00:00Z', 'open'),
			('C2', 'chore', 'lib one', 'lib-lead', '2026-10-01T00:00:00Z', 'open'),
			('D35', 'decision', 'app two', 'app-lead', '2026-10-02T00:00:00Z', 'answered')`,
		`INSERT INTO ym_item_events (item, at, from_state, to_state) VALUES ('D35', '2026-10-02T00:00:00Z', '', 'open')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	st, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if it, err := st.Item("D35"); err != nil || it.Question != "app two" {
		t.Fatalf("before renumbering, D35 = %+v, %v", it, err)
	}
	if err := st.Renumber(map[string]string{"app-lead": "/src/app/.git", "lib-lead": "/src/lib.git"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ scope, id string }{"D1": {"/src/app/.git", "D1"}, "C2": {"/src/lib.git", "C1"}, "D35": {"/src/app/.git", "D2"}}
	for alias, w := range want {
		it, err := st.Find("/src/app/.git", alias)
		if alias == "D1" {
			it, err = st.Find("/src/app/.git", "D1")
		}
		if err != nil || it.Scope != w.scope || it.ID != w.id {
			t.Errorf("%s = %s in %q (%v), want %s in %q", alias, it.ID, it.Scope, err, w.id, w.scope)
		}
	}
	it, _ := st.Find("/src/app/.git", "D35")
	if it.Spoken() != "D35" || len(it.History) != 1 {
		t.Errorf("migrated item speaks as %q with %d events, want its old ID and its history", it.Spoken(), len(it.History))
	}
	next, err := st.AddItem(Item{Scope: "/src/app/.git", Kind: KindChore, Question: "new", Session: "app-lead"})
	if err != nil || next.ID != "C3" || next.Spoken() != "C3" {
		t.Errorf("next item in app = %+v, %v; want C3", next, err)
	}
	if again, _ := st.Unscoped(); len(again) != 0 {
		t.Errorf("sessions still unscoped after renumbering: %v", again)
	}
}

func TestItemsGoInTheFilingSessionsRepoWhateverWorktreeItRunsIn(t *testing.T) {
	w := newWorld(t)
	w.core.Git = func(dir string) GitInfo {
		return map[string]GitInfo{
			"/src/app-wt-x": {Top: "/src/app-wt-x", Common: "/src/app/.git"},
			"/src/app":      {Top: "/src/app", Common: "/src/app/.git"},
		}[dir]
	}
	w.claude.cwds = map[string]string{"ended": "/src/app", "elsewhere": "/home/notes"}
	w.procs = append(w.procs, procOf(10, "claude"))
	w.claude.sessions[10] = Session{ID: "live", Cwd: "/src/app-wt-x", Status: Busy}
	for session, want := range map[string]string{"live": "/src/app/.git", "ended": "/src/app/.git", "elsewhere": "/home/notes"} {
		it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "q", Session: session})
		if err != nil || it.Scope != want {
			t.Errorf("%s filed into %q (%v), want %q", session, it.Scope, err, want)
		}
	}
}
