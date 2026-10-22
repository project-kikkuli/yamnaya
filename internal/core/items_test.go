package core

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/proc"

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
	for _, q := range []string{
		`INSERT INTO ym_items (id, kind, question, session, created_at, state) VALUES ('D36', 'decision', 'filed by an old ym', 'lib-lead', '2026-10-03T00:00:00Z', 'open')`,
		`INSERT INTO ym_item_events (item, at, from_state, to_state) VALUES ('D36', '2026-10-03T00:00:00Z', '', 'open')`,
	} {
		if _, err := st.db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.Renumber(map[string]string{"app-lead": "/src/app/.git", "lib-lead": "/src/lib.git"}); err != nil {
		t.Fatal(err)
	}
	want := map[string]struct{ scope, id string }{"D1": {"/src/app/.git", "D1"}, "C2": {"/src/lib.git", "C1"}, "D35": {"/src/app/.git", "D2"},
		"D36": {"/src/lib.git", "D2"}}
	for alias, w := range want {
		it, err := st.Find("/src/app/.git", alias)
		if alias == "D1" {
			it, err = st.Find("/src/app/.git", "D1")
		}
		if err != nil || it.Scope != w.scope || it.ID != w.id {
			t.Errorf("%s = %s in %q (%v), want %s in %q", alias, it.ID, it.Scope, err, w.id, w.scope)
		}
	}
	for _, alias := range []string{"D35", "D36"} {
		it, _ := st.Item(alias)
		if it.Spoken() != alias || len(it.History) != 1 {
			t.Errorf("migrated %s speaks as %q with %d events, want its old ID and its history", alias, it.Spoken(), len(it.History))
		}
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
		it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: "q", Session: session})
		if err != nil || it.Scope != want {
			t.Errorf("%s filed into %q (%v), want %q", session, it.Scope, err, want)
		}
	}
}

func TestASessionPrefixFilesAsTheLiveSessionItNames(t *testing.T) {
	w := newWorld(t)
	w.core.Git = func(dir string) GitInfo { return GitInfo{Top: dir, Common: "/src/app/.git"} }
	w.procs = append(w.procs, procOf(10, "claude"), procOf(11, "claude"))
	w.claude.sessions[10] = Session{ID: "c5c2b21b-51c6-4f26-b989-73dd80b07792", Cwd: "/src/app", Status: Idle}
	w.claude.sessions[11] = Session{ID: "c5c2b2ff-0000-4000-8000-000000000000", Cwd: "/src/app", Status: Idle}
	for ref, want := range map[string]string{
		"c5c2b21b":                             "c5c2b21b-51c6-4f26-b989-73dd80b07792",
		"c5c2b21b-51c6-4f26-b989-73dd80b07792": "c5c2b21b-51c6-4f26-b989-73dd80b07792",
		"0ended-session":                       "0ended-session",
	} {
		c, err := w.core.CallerOf(ref)
		if err != nil || c.Session != want {
			t.Errorf("CallerOf(%q) = %+v, %v; want session %q", ref, c, err, want)
		}
	}
	c, _ := w.core.CallerOf("c5c2b21b")
	it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: "q", Session: c.Session, Harness: c.Harness})
	if err != nil || it.Scope != "/src/app/.git" || it.Harness != "claude" {
		t.Errorf("filed %+v (%v), want it scoped to the session's repo", it, err)
	}
	if _, err := w.core.CallerOf("c5c2b2"); err == nil {
		t.Error("a prefix two live sessions share resolved, want an error")
	}
}

func TestASandboxedCodexFilesIntoItsRepoByItsThreadEnv(t *testing.T) {
	w := newWorld(t)
	w.core.Procs = func() ([]proc.Proc, error) { return nil, errors.New("exec: ps: operation not permitted") }
	w.core.Git = func(dir string) GitInfo { return GitInfo{Top: dir, Common: "/src/app/.git"} }
	w.codex.cwds = map[string]string{"thread-1": "/src/app-wt-x"}
	for _, c := range []struct {
		env     map[string]string
		want    Caller
		inShell bool
	}{
		{map[string]string{"CODEX_THREAD_ID": "thread-1"}, Caller{Harness: "codex", Session: "thread-1"}, true},
		{map[string]string{"CLAUDE_CODE_SESSION_ID": "s-1"}, Caller{Harness: "claude", Session: "s-1"}, true},
		{map[string]string{"HOME": "/home/me"}, Caller{}, false},
	} {
		got, ok := CallerFromEnv(func(k string) string { return c.env[k] })
		if ok != c.inShell || got != c.want {
			t.Errorf("CallerFromEnv(%v) = %+v, %v; want %+v, %v", c.env, got, ok, c.want, c.inShell)
		}
	}
	it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: KindDecision, Question: "q", Session: "thread-1", Harness: "codex"})
	if err != nil || it.Scope != "/src/app/.git" {
		t.Fatalf("filed %+v (%v) with no process table, want it in the thread's repo", it, err)
	}
}

func TestTheEnvironmentNamesTheCallerOnlyWhenTheProcessTableIsUnreadable(t *testing.T) {
	env := func(k string) string { return map[string]string{"CLAUDE_CODE_SESSION_ID": "inherited"}[k] }
	for _, c := range []struct {
		name  string
		procs func() ([]proc.Proc, error)
		want  Caller
	}{
		{"sandboxed", func() ([]proc.Proc, error) { return nil, errors.New("exec: ps: operation not permitted") }, Caller{Harness: "claude", Session: "inherited"}},
		{"outside any session", func() ([]proc.Proc, error) { return []proc.Proc{{PID: 7, PPID: 1, Args: []string{"zsh"}}}, nil }, Caller{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			w.core.Procs = c.procs
			got, err := w.core.Calling(7, env)
			if got != c.want || (c.want == Caller{}) != errors.Is(err, ErrNotFound) {
				t.Fatalf("Calling = %+v, %v; want %+v", got, err, c.want)
			}
		})
	}
}

func TestAGoItemAnswersWithTheGoAheadOrDismissesAndRelaysEither(t *testing.T) {
	goAhead := func(c *Core, b Board, key string) error { _, _, err := c.Go(b, key); return err }
	dismiss := func(c *Core, b Board, key string) error { _, _, err := c.Dismiss(b, key, "not now"); return err }
	for _, c := range []struct {
		name, kind, wantID, wantState, wantInbox string
		act                                      func(*Core, Board, string) error
		ok                                       bool
	}{
		{"go answers a go item", KindGo, "G1", ItemAnswered, "re G1: " + GoAhead, goAhead, true},
		{"go refuses a decision", KindDecision, "D1", ItemOpen, "", goAhead, false},
		{"dismiss closes a go item", KindGo, "G1", ItemDismissed, "re G1: dismissed: not now", dismiss, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Kind: c.kind, Question: "Build it?", Session: "lead"})
			if err != nil || it.ID != c.wantID {
				t.Fatalf("filed %s (%v), want %s", it.ID, err, c.wantID)
			}
			if err := c.act(w.core, w.board(t), it.Key()); (err == nil) != c.ok {
				t.Fatalf("act = %v, want ok %v", err, c.ok)
			}
			got, _ := w.core.Store.Item(it.Key())
			inbox, _ := w.core.Inbox("lead")
			var notices []string
			for _, in := range inbox {
				notices = append(notices, notice(in))
			}
			if got.State != c.wantState || strings.Join(notices, "\n") != c.wantInbox {
				t.Fatalf("%s %s, inbox %q; want %s, %q", got.ID, got.State, notices, c.wantState, c.wantInbox)
			}
		})
	}
}

var picks = []Option{{Label: "a", Effect: "does a"}, {Label: "b", Effect: "does b"}}
