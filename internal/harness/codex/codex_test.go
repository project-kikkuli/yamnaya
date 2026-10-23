package codex

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/core"
	"github.com/project-kikkuli/yamnaya/internal/proc"
)

const (
	busyID = "0190aaaa-1111-7111-8111-111111111111"
	idleID = "0190bbbb-2222-7222-8222-222222222222"
	newID  = "0190cccc-3333-7333-8333-333333333333"
)

type fixture struct {
	*Adapter
	open map[int][]string
}

func setup(t *testing.T) fixture {
	t.Helper()
	home := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT, created_at INTEGER, updated_at INTEGER, cwd TEXT, title TEXT,
		archived INTEGER DEFAULT 0, first_user_message TEXT DEFAULT '', name TEXT, git_branch TEXT, agent_role TEXT,
		agent_nickname TEXT, agent_path TEXT, model TEXT)`)
	exec(`CREATE TABLE thread_spawn_edges (parent_thread_id TEXT NOT NULL, child_thread_id TEXT NOT NULL PRIMARY KEY, status TEXT NOT NULL)`)
	now := time.Now()
	rollout := func(id string, lines ...string) string {
		path := filepath.Join(home, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+id+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		data := ""
		for _, l := range lines {
			data += l + "\n"
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	started := `{"type":"event_msg","payload":{"type":"task_started"}}`
	complete := `{"type":"event_msg","payload":{"type":"task_complete"}}`
	exec(`INSERT INTO threads VALUES (?, ?, 0, ?, '/Users/me/src/api', '', 0, 'add retries', 'Retry work', NULL, NULL, NULL, NULL, NULL)`, busyID, rollout(busyID, started, "garbage"), now.Add(-time.Hour).Unix())
	exec(`INSERT INTO threads VALUES (?, ?, 0, ?, '/Users/me/src/api', 'Docs pass', 0, 'docs', NULL, NULL, NULL, NULL, NULL, NULL)`, idleID, rollout(idleID, started, complete), now.Add(-time.Hour).Unix())
	exec(`INSERT INTO threads VALUES (?, ?, 0, ?, '/Users/me/src/web', '', 0, 'fresh start', NULL, NULL, NULL, NULL, NULL, NULL)`, newID, rollout(newID), now.Unix())
	f := fixture{open: map[int][]string{}}
	f.Adapter = &Adapter{
		Home:      home,
		Bin:       "codex",
		OpenFiles: func(pid int) []string { return f.open[pid] },
		Cwd:       func(pid int) string { return "/Users/me/src/web" },
		Now:       time.Now,
	}
	t.Cleanup(func() {
		if f.state != nil {
			f.state.Close()
		}
	})
	return f
}

func cp(pid int, elapsed time.Duration, args ...string) proc.Proc {
	return proc.Proc{PID: pid, PPID: 1, TTY: "/dev/ttys009", Elapsed: elapsed, Args: args}
}

func TestSessionIDFromArgvOpenFilesThenNewestThread(t *testing.T) {
	f := setup(t)
	f.open[2] = []string{"/dev/null", filepath.Join(f.Home, "sessions", "2026", "01", "02", "rollout-2026-01-02T03-04-05-"+idleID+".jsonl")}
	f.open[3] = []string{filepath.Join(f.Home, "thread-writer-locks", busyID+".lock")}
	cases := []struct {
		proc proc.Proc
		want string
	}{
		{cp(1, time.Hour, "codex", "resume", busyID), busyID},
		{cp(5, time.Hour, "codex", "resume", "--add-dir", "/s", "-c", `shell_environment_policy.set.X="/s"`, idleID), idleID},
		{cp(2, time.Hour, "codex"), idleID},
		{cp(3, time.Hour, "codex"), busyID},
		{cp(4, time.Minute, "codex"), newID},
	}
	for _, c := range cases {
		if got := f.SessionID(c.proc); got != c.want {
			t.Errorf("pid %d: SessionID = %q, want %q", c.proc.PID, got, c.want)
		}
	}
}

func TestCollidingThreadPrefixesKeepEachProcessOnItsOwnRollout(t *testing.T) {
	ids := []string{"01a16a70-a4c4-7670-a0a2-e092c1f5e145", "01a16a70-a4c4-7670-a0a2-e0a21f96395f"}
	f := setup(t)
	db, err := sql.Open("sqlite", filepath.Join(f.Home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for i, old := range []string{busyID, idleID} {
		oldPath := f.Transcript(old)
		path := filepath.Join(filepath.Dir(oldPath), "rollout-2026-01-02T03-04-05-"+ids[i]+".jsonl")
		if err := os.Rename(oldPath, path); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE threads SET id = ?, rollout_path = ? WHERE id = ?`, ids[i], path, old); err != nil {
			t.Fatal(err)
		}
		f.open[i+1] = []string{path}
	}
	procs := []proc.Proc{cp(1, time.Hour, "codex"), cp(2, time.Hour, "codex")}
	got := f.Discover(procs)
	for i, p := range procs {
		if id := f.SessionID(p); id != ids[i] || got[i].ID != id || got[i].PID != p.PID {
			t.Fatalf("pid %d: SessionID %s, discovered %+v, want %s", p.PID, id, got[i], ids[i])
		}
		if !strings.Contains(f.Transcript(ids[i]), ids[i]) {
			t.Fatalf("thread %s lost its own rollout", ids[i])
		}
	}
	if got[0].Title != "Retry work" || got[1].Title != "Docs pass" || got[0].Status != core.Busy || got[1].Status != core.Idle {
		t.Fatalf("rollout facts crossed sessions: %+v", got)
	}
}

func TestDiscoverReadsTitleStatusAndAsksForScreenWhenBusy(t *testing.T) {
	f := setup(t)
	got := f.Discover([]proc.Proc{
		cp(1, time.Hour, "codex", "resume", busyID),
		cp(2, time.Hour, "codex", "resume", idleID),
	})
	busy, idle := got[0], got[1]
	if busy.ID != busyID || busy.Title != "Retry work" || busy.Status != core.Busy || !busy.CheckScreen || busy.Cwd != "/Users/me/src/api" {
		t.Errorf("busy = %+v", busy)
	}
	if idle.Title != "Docs pass" || idle.Status != core.Idle || idle.CheckScreen {
		t.Errorf("idle = %+v", idle)
	}
}

func TestDiscoverNeverGuessesAThreadAnotherProcessNames(t *testing.T) {
	f := setup(t)
	got := f.Discover([]proc.Proc{cp(1, time.Minute, "codex"), cp(2, time.Minute, "codex", "resume", newID)})
	if got[0].ID == newID || got[1].ID != newID {
		t.Fatalf("ids = %q, %q", got[0].ID, got[1].ID)
	}
}

func TestDiscoverFailsSoftWithoutState(t *testing.T) {
	a := &Adapter{Home: t.TempDir(), Cwd: func(int) string { return "/Users/me/src/web" }, Now: time.Now}
	got := a.Discover([]proc.Proc{cp(9, time.Minute, "codex")})
	if len(got) != 1 || got[0].ID != "" || got[0].Status != core.Unknown || got[0].Cwd != "/Users/me/src/web" {
		t.Fatalf("got %+v", got)
	}
}

func TestOwnsInteractiveAndExecButNotServers(t *testing.T) {
	a := &Adapter{}
	cases := map[string]struct {
		args []string
		want bool
	}{
		"tui":        {[]string{"codex"}, true},
		"resume":     {[]string{"/opt/bin/codex", "resume", busyID}, true},
		"exec":       {[]string{"codex", "exec", "do it"}, true},
		"app server": {[]string{"codex", "app-server", "--listen", "unix://"}, false},
		"mcp":        {[]string{"codex", "mcp-server"}, false},
		"claude":     {[]string{"claude"}, false},
	}
	for name, c := range cases {
		if got := a.Owns(proc.Proc{Args: c.args}); got != c.want {
			t.Errorf("%s: Owns = %v, want %v", name, got, c.want)
		}
	}
}

func TestParsePromptFromRealDialogs(t *testing.T) {
	cases := map[string]core.Prompt{
		"codex-exec":           {Tool: "Shell", Detail: "rm -rf build && mkdir build"},
		"codex-patch":          {Tool: "Edit", Detail: "/Users/me/tmp/ymperm-codex/notes.txt"},
		"codex-trust":          {Tool: "trust folder", Detail: "/Users/me/tmp/ymperm-codex"},
		"codex-trust-2":        {Tool: "trust folder", Detail: "/private/tmp/ymcx.3AJg/repo/sub"},
		"codex-open-elsewhere": {Tool: "open in another app"},
	}
	for name, want := range cases {
		b, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := ParsePrompt(string(b))
		if !ok || got != want {
			t.Errorf("%s: ParsePrompt = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	if got, ok := ParsePrompt("› Would you like to run the following command?\n• sure\n"); ok {
		t.Errorf("quoted prompt read as a dialog: %+v", got)
	}
}

func TestSpawnEdgesMakeCrewAndNeverAnswerForTheRoot(t *testing.T) {
	const root, kid, grandkid = "0190dddd-4444-7444-8444-444444444444", "0190eeee-5555-7555-8555-555555555555", "0190ffff-6666-7666-8666-666666666666"
	f := setup(t)
	db, err := sql.Open("sqlite", filepath.Join(f.Home, "state_5.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	write := func(id string, lines ...string) string {
		path := filepath.Join(f.Home, "sessions", "rollout-"+id+".jsonl")
		data := ""
		for _, l := range lines {
			data += l + "\n"
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	now := time.Now().Unix()
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO threads VALUES (?, ?, 0, ?, '/Users/me/src/web', 'Ship workstations', 0, 'ship workstations', NULL, 'main', NULL, NULL, NULL, 'gpt-6')`,
			[]any{root, write(root,
				`{"type":"event_msg","payload":{"type":"task_started"}}`,
				`{"type":"event_msg","payload":{"type":"user_message","message":"ship workstations"}}`,
				`{"type":"response_item","timestamp":"2026-10-15T10:00:00Z","payload":{"type":"custom_tool_call","name":"exec","call_id":"c1","input":"await tools.exec_command({cmd:\"gh pr create --fill\"})"}}`,
				`{"type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"c1","output":"https://github.com/me/web/pull/9"}}`,
			), now + 1}},
		{`INSERT INTO threads VALUES (?, ?, 0, ?, '/Users/me/src/web', '', 0, '', NULL, 'main', NULL, 'Hubble', '/root/shepherd', 'gpt-6')`,
			[]any{kid, write(kid,
				`{"type":"event_msg","payload":{"type":"task_started"}}`,
				`{"type":"response_item","payload":{"type":"agent_message","content":[{"type":"input_text","text":"Task name: /root/shepherd"},{"type":"encrypted_content","encrypted_content":"x"}]}}`,
				`{"type":"response_item","payload":{"type":"custom_tool_call","name":"apply_patch","call_id":"p1","input":"*** Begin Patch\n*** Update File: lib/vms.py\n*** End Patch"}}`,
				`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"Shepherded."}]}}`,
				`{"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"You are /root/shepherd. Summary of the work so far."}]}}`,
				`{"type":"compacted","payload":{"message":"Another language model started to solve this problem and produced a summary of its thinking process."}}`,
			), now + 2}},
		{`INSERT INTO threads VALUES (?, ?, 0, ?, '/Users/me/src/web', '', 0, '', NULL, 'main', 'reviewer', NULL, '/root/shepherd/review', NULL)`,
			[]any{grandkid, write(grandkid, `{"type":"event_msg","payload":{"type":"task_complete"}}`), now + 2}},
		{`INSERT INTO thread_spawn_edges VALUES (?, ?, 'open')`, []any{root, kid}},
		{`INSERT INTO thread_spawn_edges VALUES (?, ?, 'closed')`, []any{kid, grandkid}},
	} {
		if _, err := db.Exec(q.sql, q.args...); err != nil {
			t.Fatal(err)
		}
	}
	f.open[5] = []string{filepath.Join(f.Home, "sessions", "rollout-"+kid+".jsonl"), filepath.Join(f.Home, "sessions", "rollout-"+root+".jsonl")}
	for name, pr := range map[string]proc.Proc{
		"holds its subagent's rollout first":         cp(5, time.Hour, "codex"),
		"holds nothing, newest thread is a subagent": cp(6, time.Hour, "codex"),
	} {
		s := f.Discover([]proc.Proc{pr})[0]
		if s.ID != root {
			t.Errorf("%s: resolved %q, want the root thread", name, s.ID)
			continue
		}
		if s.Task != "ship workstations" || s.Doing.String() != "Bash: gh pr create --fill" || len(s.PRs) != 1 || s.PRs[0].Number != 9 {
			t.Errorf("%s: facts = %+v", name, s.Facts)
		}
		if len(s.Crew) != 2 {
			t.Fatalf("%s: crew = %+v, want the child and grandchild", name, s.Crew)
		}
		crew := map[string]core.Member{}
		for _, m := range s.Crew {
			crew[m.ID] = m
		}
		hub, rev := crew[kid], crew[grandkid]
		if hub.Name != "Hubble" || hub.Status != core.Busy || hub.Result != "Shepherded." || !strings.Contains(hub.Brief, "Task name: /root/shepherd") ||
			len(hub.Files) != 1 || hub.Files[0] != "/Users/me/src/web/lib/vms.py" {
			t.Errorf("%s: child = %+v", name, hub)
		}
		if rev.Name != "reviewer" || rev.Status != core.Done {
			t.Errorf("%s: grandchild = %+v", name, rev)
		}
	}
}

func TestOpenElsewhereReadsOnlyTheLiveNotice(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "codex-open-elsewhere.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		screen string
		want   bool
	}{
		{string(b), true},
		{"› This conversation is open in another app\n• noted\n› Ask Codex to do anything\n", false},
	} {
		if got := OpenElsewhere(c.screen); got != c.want {
			t.Errorf("OpenElsewhere(%q) = %v, want %v", c.screen, got, c.want)
		}
	}
}

func TestComposerReadsTheInputBoxOnlyWhenNoDialogShows(t *testing.T) {
	cases := map[string]struct {
		box string
		ok  bool
	}{
		"codex-idle":           {"", true},
		"codex-busy-pasted":    {"marker alpha $HOME `pwd` \"quoted\" 'single' end", true},
		"codex-busy-long":      {"marker alpha $HOME `pwd` \"quoted\" 'single' end[Pasted Content 2289 chars]", true},
		"codex-approval":       {"", false},
		"codex-exec":           {"", false},
		"codex-trust":          {"", false},
		"codex-open-elsewhere": {"", false},
	}
	for name, want := range cases {
		b, err := os.ReadFile(filepath.Join("testdata", name+".txt"))
		if err != nil {
			t.Fatal(err)
		}
		if box, ok := Composer(string(b)); box != want.box || ok != want.ok {
			t.Errorf("%s: Composer = %q, %v; want %q, %v", name, box, ok, want.box, want.ok)
		}
	}
}

func TestComposerReadsParagraphsAndIdleDetectionUsesTheLiveTurn(t *testing.T) {
	for _, c := range []struct {
		name, screen, box string
		idle              bool
	}{
		{"empty placeholder", "› Ask Codex to do anything\n\n  GPT-6.1-Sol high fast · /repo\n  ← for agents · ? for shortcuts\n", "", true},
		{"multiple paragraphs", "› audit done\n  \n  - passed locks\n  - reproduced race\n  \n  no credentials changed\n\n  GPT-6.1-Sol high fast · /repo\n  ← for agents · ? for shortcuts\n", "audit done  - passed locks - reproduced race  no credentials changed", true},
		{"working with normal composer", "• Working (2m • esc to interrupt)\n\n› Ask Codex to do anything\n\n  GPT-6.1-Sol high fast · /repo\n  ← for agents · ? for shortcuts\n", "", false},
		{"busy queued message", "• Working (2m • esc to interrupt)\n\n› text\n\n  GPT-6.1-Sol high fast · /repo\n  tab to queue message\n", "text", false},
		{"old working line in scrollback", "• Working (2m • esc to interrupt)\n• Done.\n\n› Ask Codex to do anything\n\n  GPT-6.1-Sol high fast · /repo\n  ← for agents · ? for shortcuts\n", "", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			box, ok := Composer(c.screen)
			if !ok || box != c.box {
				t.Fatalf("Composer = %q, %v; want %q", box, ok, c.box)
			}
			_, idle := (&Adapter{}).IdleComposer(c.screen)
			if idle != c.idle {
				t.Fatalf("idle=%v, want %v", idle, c.idle)
			}
		})
	}
}
