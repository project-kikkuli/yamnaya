package discover

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/harness"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
)

var now = time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)

const cwd = "/Users/me/src/kiln"

type fixture struct {
	t      *testing.T
	d      *Discoverer
	panes  []tmux.PaneInfo
	procs  []Proc
	cwds   map[int]string
	lsofed []int
}

func newFixture(t *testing.T) *fixture {
	f := &fixture{t: t, cwds: map[int]string{}}
	root := t.TempDir()
	f.d = &Discoverer{
		ClaudeDir: filepath.Join(root, "claude"),
		CodexHome: filepath.Join(root, "codex"),
		Panes:     func() ([]tmux.PaneInfo, error) { return f.panes, nil },
		Procs:     func() ([]Proc, error) { return f.procs, nil },
		Cwds: func(pids []int) map[int]string {
			f.lsofed = append(f.lsofed, pids...)
			return f.cwds
		},
		Git: func(dir string) (string, string) { return filepath.Base(dir), "main" },
		Now: func() time.Time { return now },
	}
	return f
}

// pane adds a tmux pane whose shell runs argv as a child process.
func (f *fixture) pane(id string, shellPID int, argv ...string) {
	f.panes = append(f.panes, tmux.PaneInfo{ID: id, WindowID: "@" + id[1:], Session: "main", WindowIndex: len(f.panes), WindowName: "w" + id[1:], PID: shellPID, Cwd: cwd})
	f.procs = append(f.procs, Proc{PID: shellPID, PPID: 1, Args: []string{"-fish"}})
	if len(argv) > 0 {
		f.procs = append(f.procs, Proc{PID: shellPID + 1, PPID: shellPID, Elapsed: time.Hour, Args: argv})
		f.procs = append(f.procs, Proc{PID: shellPID + 2, PPID: shellPID + 1, Elapsed: time.Minute, Args: []string{"/bin/zsh", "-c", "claude --version"}})
	}
}

func (f *fixture) write(path string, lines ...any) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		if s, ok := l.(string); ok {
			b.WriteString(s)
		} else {
			j, _ := json.Marshal(l)
			b.Write(j)
		}
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) touch(path string, at time.Time) {
	if err := os.Chtimes(path, at, at); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) state(pid int, id, status string, updated time.Time, extra map[string]any) {
	st := map[string]any{"pid": pid, "sessionId": id, "cwd": cwd, "status": status, "statusUpdatedAt": updated.UnixMilli(), "name": "kiln-1a", "nameSource": "derived"}
	for k, v := range extra {
		st[k] = v
	}
	f.write(filepath.Join(f.d.ClaudeDir, "sessions", fmt.Sprintf("%d.json", pid)), st)
}

func (f *fixture) transcript(id string, lines ...any) string {
	p := filepath.Join(f.d.ClaudeDir, "projects", "-Users-me-src-kiln", id+".jsonl")
	f.write(p, lines...)
	return p
}

func (f *fixture) discover() []Session {
	f.t.Helper()
	ss, err := f.d.Discover()
	if err != nil {
		f.t.Fatal(err)
	}
	return ss
}

func user(text string) map[string]any {
	return map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}
}

func assistant(stop any, blocks ...string) map[string]any {
	content := []map[string]any{}
	for _, b := range blocks {
		content = append(content, map[string]any{"type": b})
	}
	return map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "stop_reason": stop, "content": content}}
}

var turnDone = map[string]any{"type": "system", "subtype": "turn_duration", "durationMs": 900}

func TestDiscoverFindsTheOutermostHarnessInEachLivePane(t *testing.T) {
	f := newFixture(t)
	f.pane("%1", 100, "claude", "--chrome")
	f.pane("%2", 200, "node", "/opt/lib/codex/bin/codex")
	f.pane("%3", 300)
	f.pane("%4", 400, "claude")
	f.panes[3].Dead = true
	f.pane("%5", 500, "vim", "claude.md")
	f.state(101, "s-one", "busy", now, nil)

	ss := f.discover()
	var got []string
	for _, s := range ss {
		got = append(got, fmt.Sprintf("%s %s %d", s.Pane, s.Harness, s.PID))
	}
	want := []string{"%1 claude 101", "%2 codex 201"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("sessions = %v, want %v", got, want)
	}
	if len(f.lsofed) != 1 || f.lsofed[0] != 201 {
		t.Fatalf("lsof asked for %v; claude cwd comes from its session state", f.lsofed)
	}
	if ss[0].Repo != "kiln" || ss[0].Branch != "main" || ss[0].Cwd != cwd {
		t.Fatalf("repo = %q/%q cwd %q", ss[0].Repo, ss[0].Branch, ss[0].Cwd)
	}
}

func TestClaudeStatus(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		updated    time.Time
		lines      []any
		modified   time.Time
		want       Status
		waitingFor string
	}{
		{name: "busy is running", status: "busy", updated: now.Add(-time.Minute), want: Running},
		{name: "waiting needs you", status: "waiting", updated: now.Add(-time.Minute), want: NeedsYou, waitingFor: "permission"},
		{name: "fresh idle is idle", status: "idle", updated: now.Add(-5 * time.Minute), want: Idle},
		{name: "old idle is idle", status: "idle", updated: now.Add(-2 * time.Hour), want: Idle},
		{name: "no state, open turn", lines: []any{user("fix the kiln"), assistant("tool_use", "tool_use")}, modified: now.Add(-time.Minute), want: Running},
		{name: "no state, finished turn", lines: []any{user("fix the kiln"), assistant("end_turn", "text"), turnDone}, modified: now.Add(-time.Minute), want: YourTurn},
		{name: "no state, stale open turn", lines: []any{user("fix the kiln"), assistant("tool_use", "tool_use")}, modified: now.Add(-time.Hour), want: Idle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.pane("%1", 100, "claude", "--resume", "s-one")
			if c.status != "" {
				extra := map[string]any{}
				if c.waitingFor != "" {
					extra["waitingFor"] = c.waitingFor
				}
				f.state(101, "s-one", c.status, c.updated, extra)
			}
			lines := c.lines
			if lines == nil {
				lines = []any{user("fix the kiln")}
			}
			p := f.transcript("s-one", lines...)
			if !c.modified.IsZero() {
				f.touch(p, c.modified)
			}
			s := f.discover()[0]
			if s.ID != "s-one" || s.Status != c.want || s.WaitingFor != c.waitingFor {
				t.Fatalf("got id %q status %v waiting %q; want %v %q", s.ID, s.Status, s.WaitingFor, c.want, c.waitingFor)
			}
		})
	}
}

func TestClaudeSessionIDFallsBackToTheTranscriptWrittenSinceTheProcessStarted(t *testing.T) {
	f := newFixture(t)
	f.pane("%1", 100, "claude")
	f.touch(f.transcript("older", user("old work")), now.Add(-3*time.Hour))
	f.touch(f.transcript("newer", user("new work")), now.Add(-time.Minute))
	s := f.discover()[0]
	if s.ID != "newer" || s.Title != "new work" {
		t.Fatalf("id %q title %q", s.ID, s.Title)
	}
}

func TestClaudeTitlePrefersNamesOverPrompts(t *testing.T) {
	cases := []struct {
		name  string
		lines []any
		want  string
	}{
		{"first typed prompt", []any{
			user("<command-name>/clear</command-name>"),
			map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"content": "caveat"}},
			map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "tool_result"}}}},
			map[string]any{"type": "user", "message": map[string]any{"content": []map[string]any{{"type": "text", "text": "glaze  the\nbowls"}}}},
			user("second prompt"),
		}, "glaze the bowls"},
		{"ai title beats prompt", []any{user("glaze"), map[string]any{"type": "ai-title", "aiTitle": "Old title"}, map[string]any{"type": "ai-title", "aiTitle": "Glazing bowls"}}, "Glazing bowls"},
		{"agent name beats ai title", []any{map[string]any{"type": "agent-name", "agentName": "glazer"}, map[string]any{"type": "ai-title", "aiTitle": "Glazing bowls"}}, "glazer"},
		{"custom title wins", []any{map[string]any{"type": "custom-title", "customTitle": "bowls"}, map[string]any{"type": "agent-name", "agentName": "glazer"}}, "bowls"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.pane("%1", 100, "claude")
			f.state(101, "s-one", "idle", now, nil)
			f.transcript("s-one", c.lines...)
			if got := f.discover()[0].Title; got != c.want {
				t.Fatalf("title = %q, want %q", got, c.want)
			}
		})
	}
}

func TestClaudeTitleFollowsAppendedRecords(t *testing.T) {
	f := newFixture(t)
	f.pane("%1", 100, "claude")
	f.state(101, "s-one", "idle", now, nil)
	p := f.transcript("s-one", user("glaze the bowls"))
	if got := f.discover()[0].Title; got != "glaze the bowls" {
		t.Fatalf("title = %q", got)
	}
	fh, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(fh, `{"type":"ai-title","aiTitle":"Glazing bowls"}`)
	fmt.Fprint(fh, `{"type":"custom-title","customTitle":"half-writ`)
	fh.Close()
	if got := f.discover()[0].Title; got != "Glazing bowls" {
		t.Fatalf("title after append = %q", got)
	}
}

func TestCrewStatus(t *testing.T) {
	type agent struct {
		id       string
		meta     map[string]any
		last     any
		modified time.Time
	}
	teammate := func(name string) map[string]any {
		return map[string]any{"agentType": name, "name": name, "description": "build " + name, "taskKind": "in_process_teammate"}
	}
	background := func(desc string) map[string]any {
		return map[string]any{"agentType": "Explore", "description": desc}
	}
	agents := []agent{
		{"aglazer-1", teammate("glazer"), assistant("tool_use", "tool_use"), now.Add(-10 * time.Second)},
		{"afirer-2", teammate("firer"), user("<teammate-message>fire it</teammate-message>"), now.Add(-time.Minute)},
		{"athrower-3", teammate("thrower"), assistant("end_turn", "text"), now.Add(-20 * time.Minute)},
		{"a4", background("Survey clay stock"), assistant("tool_use", "tool_use"), now.Add(-30 * time.Second)},
		{"a5", background("Price kilns"), assistant("end_turn", "text"), now.Add(-time.Hour)},
		{"a6", background("Stalled audit"), assistant("tool_use", "tool_use"), now.Add(-time.Hour)},
		{"a7", background("Streaming reply"), assistant(nil, "text"), now.Add(-10 * time.Second)},
	}
	cases := []struct {
		name    string
		session string
		want    map[string]CrewStatus
	}{
		{"session running", "busy", map[string]CrewStatus{
			"glazer": CrewRunning, "firer": CrewRunning, "thrower": CrewIdle,
			"Survey clay stock": CrewRunning, "Price kilns": CrewDone, "Stalled audit": CrewDone, "Streaming reply": CrewRunning,
		}},
		{"session idle has no running crew", "idle", map[string]CrewStatus{
			"glazer": CrewIdle, "firer": CrewIdle, "thrower": CrewIdle,
			"Survey clay stock": CrewDone, "Price kilns": CrewDone, "Stalled audit": CrewDone, "Streaming reply": CrewDone,
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.pane("%1", 100, "claude")
			f.state(101, "s-one", c.session, now, nil)
			p := f.transcript("s-one", user("run the studio"))
			dir := filepath.Join(strings.TrimSuffix(p, ".jsonl"), "subagents")
			for _, a := range agents {
				f.write(filepath.Join(dir, "agent-"+a.id+".meta.json"), a.meta)
				tp := filepath.Join(dir, "agent-"+a.id+".jsonl")
				f.write(tp, user("brief"), a.last)
				f.touch(tp, a.modified)
			}
			s := f.discover()[0]
			got := map[string]CrewStatus{}
			for _, m := range s.Crew {
				got[m.Label()] = m.Status
			}
			if fmt.Sprint(got) != fmt.Sprint(c.want) {
				t.Fatalf("crew = %v\nwant   %v", got, c.want)
			}
			for i := 1; i < len(s.Crew); i++ {
				if s.Crew[i].Status < s.Crew[i-1].Status {
					t.Fatalf("crew not ordered running, idle, done: %v", s.Crew)
				}
			}
		})
	}
}

func (f *fixture) codexDB(threads ...[]any) {
	f.t.Helper()
	if err := os.MkdirAll(f.d.CodexHome, 0o755); err != nil {
		f.t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.d.CodexHome, "state_5.sqlite"))
	if err != nil {
		f.t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, updated_at INTEGER, cwd TEXT, title TEXT, archived INTEGER, first_user_message TEXT, name TEXT)`); err != nil {
		f.t.Fatal(err)
	}
	for _, th := range threads {
		if _, err := db.Exec(`INSERT INTO threads VALUES (?, ?, ?, ?, ?, 0, ?, ?)`, th...); err != nil {
			f.t.Fatal(err)
		}
	}
}

func event(kind string) map[string]any {
	return map[string]any{"type": "event_msg", "payload": map[string]any{"type": kind}}
}

func TestCodexThreadAndStatus(t *testing.T) {
	cases := []struct {
		name   string
		argv   []string
		lock   string
		events []any
		wantID string
		title  string
		want   Status
	}{
		{"writer lock picks the live thread", []string{"codex"}, "t-old", []any{event("task_started")}, "t-old", "Fire the kiln", Running},
		{"resume arg wins", []string{"codex", "resume", "t-new-00000000000000000000000000000"}, "t-old", []any{event("task_started"), event("task_complete")}, "t-new-00000000000000000000000000000", "glaze bowls", YourTurn},
		{"newest since start without a lock", []string{"codex"}, "", []any{event("task_started"), event("turn_aborted")}, "t-new-00000000000000000000000000000", "glaze bowls", YourTurn},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.pane("%1", 100, c.argv...)
			f.cwds[101] = cwd
			rollout := func(id string) string {
				p := filepath.Join(f.d.CodexHome, "sessions", "rollout-"+id+".jsonl")
				f.write(p, append([]any{map[string]any{"type": "session_meta"}}, c.events...)...)
				f.touch(p, now.Add(-time.Minute))
				return p
			}
			f.codexDB(
				[]any{"t-old", rollout("t-old"), now.Add(-2 * time.Hour).Unix(), cwd, "", "fire the kiln", "Fire the kiln"},
				[]any{"t-new-00000000000000000000000000000", rollout("t-new"), now.Add(-time.Minute).Unix(), cwd, "", "glaze bowls", ""},
				[]any{"t-elsewhere", rollout("t-elsewhere"), now.Unix(), "/Users/me/src/other", "Other", "", ""},
			)
			if c.lock != "" {
				f.write(filepath.Join(f.d.CodexHome, "thread-writer-locks", c.lock+".lock"))
			}
			s := f.discover()[0]
			if s.Harness != harness.Codex || s.ID != c.wantID || s.Title != c.title || s.Status != c.want {
				t.Fatalf("got %s %q %q %v; want %q %q %v", s.Harness, s.ID, s.Title, s.Status, c.wantID, c.title, c.want)
			}
		})
	}
}

func TestParsePS(t *testing.T) {
	procs := ParsePS("  101   100 02-03:04:05 claude --chrome --resume x\n 7 1 00:09 /bin/zsh -l\nbad line\n")
	if len(procs) != 2 {
		t.Fatalf("procs = %v", procs)
	}
	want := 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second
	if p := procs[0]; p.PID != 101 || p.PPID != 100 || p.Elapsed != want || argSessionID(p.Args) != "x" {
		t.Fatalf("proc = %+v", p)
	}
	if procs[1].Elapsed != 9*time.Second {
		t.Fatalf("elapsed = %v", procs[1].Elapsed)
	}
}
