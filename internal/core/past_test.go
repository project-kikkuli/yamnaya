package core

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// codexState writes a Codex state database holding threads, each id, cwd, rollout, updated ms, first message, title, source, archived.
func codexState(t *testing.T, dir string, threads ...[]any) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, cwd TEXT NOT NULL, rollout_path TEXT NOT NULL, updated_at INTEGER NOT NULL,
		updated_at_ms INTEGER, first_user_message TEXT NOT NULL DEFAULT '', title TEXT NOT NULL, thread_source TEXT, archived INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	for _, th := range threads {
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?, ?, 0, ?, ?, ?, ?, ?)`, th...); err != nil {
			t.Fatal(err)
		}
	}
}

func TestReadPastListsEachHarnessesConversationsNewestFirst(t *testing.T) {
	ms := func(s int64) time.Time { return time.UnixMilli(s * 1000) }
	for _, c := range []struct {
		name  string
		setup func(t *testing.T, d PastDirs)
		want  []Past
	}{
		{"no history at all", func(t *testing.T, d PastDirs) {}, nil},
		{"claude prompts gather by session, transcript found under the project", func(t *testing.T, d PastDirs) {
			writeLines(t, filepath.Join(d.Claude, "history.jsonl"),
				`{"display":"fix the\nlogin","timestamp":1000000,"project":"/src/app","sessionId":"c1"}`,
				`not json`,
				`{"display":"/status","timestamp":3000000,"project":"/src/other","sessionId":"c2"}`,
				`{"display":"and its tests","timestamp":2000000,"project":"/src/app","sessionId":"c1"}`,
				`{"display":"no session","timestamp":4000000,"project":"/src/app"}`)
			writeLines(t, filepath.Join(d.Claude, "projects", "-src-app", "c1.jsonl"), `{}`)
		}, []Past{
			{Harness: "claude", Session: "c2", Cwd: "/src/other", Last: ms(3000), Prompts: []string{"/status"}},
			{Harness: "claude", Session: "c1", Cwd: "/src/app", Last: ms(2000), Prompts: []string{"fix the login", "and its tests"}},
		}},
		{"codex threads take their prompts from history, else the first message, skipping subagents and archived", func(t *testing.T, d PastDirs) {
			rollout := filepath.Join(d.Codex, "sessions", "x1.jsonl")
			writeLines(t, rollout, `{}`)
			writeLines(t, filepath.Join(d.Codex, "history.jsonl"),
				`{"session_id":"x1","ts":1,"text":"port the cli"}`,
				`{"session_id":"x1","ts":2,"text":"ship it"}`)
			codexState(t, d.CodexState,
				[]any{"x1", "/src/cli", rollout, 5000000, "port the cli", "Port CLI", nil, 0},
				[]any{"x2", "/src/web", "/gone.jsonl", 6000000, "style\nthe page", "", "cli", 0},
				[]any{"x3", "/src/web", rollout, 7000000, "help", "", "subagent", 0},
				[]any{"x4", "/src/web", rollout, 8000000, "old", "", nil, 1})
		}, []Past{
			{Harness: "codex", Session: "x2", Cwd: "/src/web", Last: ms(6000), Prompts: []string{"style the page"}},
			{Harness: "codex", Session: "x1", Cwd: "/src/cli", Title: "Port CLI", Last: ms(5000), Prompts: []string{"port the cli", "ship it"}},
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			d := PastDirs{Claude: filepath.Join(root, "claude"), Codex: filepath.Join(root, "codex"), CodexState: filepath.Join(root, "state")}
			for _, dir := range []string{d.Claude, d.Codex, d.CodexState} {
				os.MkdirAll(dir, 0o755)
			}
			c.setup(t, d)
			got, err := ReadPast(d)
			if err != nil {
				t.Fatal(err)
			}
			for i := range got {
				if got[i].Transcript != "" && !strings.HasPrefix(got[i].Transcript, root) {
					t.Errorf("%s transcript %q lies outside the fixture", got[i].Session, got[i].Transcript)
				}
				if (got[i].Transcript != "") != (got[i].Session == "c1" || got[i].Session == "x1") {
					t.Errorf("%s transcript %q", got[i].Session, got[i].Transcript)
				}
				got[i].Transcript = ""
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got  %+v\nwant %+v", got, c.want)
			}
		})
	}
}

func TestFilterPastMatchesEveryWordExactly(t *testing.T) {
	ps := []Past{
		{Session: "a", Cwd: "/src/yamnaya", Prompts: []string{"fix the Login flow", "add tests"}},
		{Session: "b", Cwd: "/src/web", Prompts: []string{"login page styles"}},
		{Session: "c", Cwd: "/src/web", Prompts: []string{strings.Repeat("x", PastTextCap) + " beyond"}},
	}
	for _, c := range []struct {
		query string
		want  []string
	}{
		{"", []string{"a", "b", "c"}},
		{"login", []string{"a", "b"}},
		{"Login", []string{"a"}},
		{"login tests", []string{"a"}},
		{"  web   login ", []string{"b"}},
		{"yamnaya", []string{"a"}},
		{"logn", nil},
		{"beyond", nil},
	} {
		t.Run(c.query, func(t *testing.T) {
			var got []string
			for _, p := range FilterPast(ps, c.query) {
				got = append(got, p.Session)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// relaunches records each session Relaunch resumes, as its key, harness, id and directory.
type relaunches struct {
	*fakeLaunches
	relaunched []string
}

func (l *relaunches) Relaunch(from Live, harness, id string) (string, error) {
	l.relaunched = append(l.relaunched, from.Key()+" "+harness+" "+id+" "+from.Cwd)
	return "revived", nil
}

func TestResumePastRelaunchesItInItsDirectory(t *testing.T) {
	for _, c := range []struct {
		name    string
		p       Past
		want    []string
		wantErr string
	}{
		{"with its transcript", Past{Harness: "codex", Session: "x1", Cwd: "/src/cli", Transcript: "/t/x1.jsonl"}, []string{"x1 codex x1 /src/cli"}, ""},
		{"transcript gone", Past{Harness: "claude", Session: "c1", Cwd: "/src/app"}, nil, "no transcript on disk"},
	} {
		t.Run(c.name, func(t *testing.T) {
			l := &relaunches{fakeLaunches: &fakeLaunches{}}
			core := &Core{Launches: l}
			_, err := core.ResumePast(c.p)
			if c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)) || c.wantErr == "" && err != nil {
				t.Fatalf("err %v, want %q", err, c.wantErr)
			}
			if !reflect.DeepEqual(l.relaunched, c.want) {
				t.Fatalf("relaunched %q, want %q", l.relaunched, c.want)
			}
		})
	}
}

func TestUnlaunchedLeavesWhatTheBoardShows(t *testing.T) {
	ps := []Past{{Session: "launched"}, {Session: "live"}, {Session: "dormant"}, {Session: "other"}}
	b := Board{Launched: []Launched{{Session: "launched"}}, Sessions: []Live{{Session: Session{ID: "live"}}},
		Projects: []Project{{Dormant: []Dormant{{Session: "dormant"}}}}}
	if got := Unlaunched(ps, b); len(got) != 1 || got[0].Session != "other" || len(ps) != 4 {
		t.Fatalf("got %+v from %+v", got, ps)
	}
}
