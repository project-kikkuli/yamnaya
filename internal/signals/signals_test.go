package signals

import (
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/discover"
	"github.com/project-kikkuli/yamnaya/internal/store"
	"github.com/project-kikkuli/yamnaya/internal/tmux"
)

var socketSeq atomic.Int64

type fixture struct {
	t      *testing.T
	dir    string
	store  *store.Store
	tmux   tmux.Client
	reader *Reader
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	tc := tmux.Client{Socket: fmt.Sprintf("ymsig-%d-%d", os.Getpid(), socketSeq.Add(1))}
	t.Cleanup(tc.KillServer)
	r := NewReader(s, tc)
	r.Transcripts = &Transcripts{ClaudeDir: filepath.Join(dir, "claude"), CodexHome: filepath.Join(dir, "codex")}
	return &fixture{t: t, dir: dir, store: s, tmux: tc, reader: r}
}

func (f *fixture) launch(project string) func(store.Window) (string, error) {
	return func(w store.Window) (string, error) {
		return f.tmux.NewWindow(tmux.SessionName(project), w.Name, strconv.FormatInt(w.ID, 10), f.dir, nil, []string{"sleep", "600"})
	}
}

func (f *fixture) project(name string, windows ...store.Window) {
	f.t.Helper()
	manager := store.Window{Project: name, Name: "manager", Role: store.RoleManager, Harness: "claude", SessionID: name + "-mgr", Managed: true}
	if err := f.store.CreateProject(store.Project{Name: name, Cwd: f.dir}, manager, f.launch(name)); err != nil {
		f.t.Fatal(err)
	}
	for _, w := range windows {
		w.Project, w.Role, w.Managed = name, store.RoleWorker, true
		if err := f.store.AddWindow(w, f.launch(name)); err != nil {
			f.t.Fatal(err)
		}
	}
}

func (f *fixture) item(project, source string) {
	f.t.Helper()
	if _, err := f.store.AddItem(store.Item{Project: project, Kind: store.KindDecision, Question: "q", SourceWindow: source}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) transcript(path string, age time.Duration) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
		f.t.Fatal(err)
	}
	mt := time.Now().Add(-age)
	if err := os.Chtimes(path, mt, mt); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) snapshot() Snapshot {
	f.t.Helper()
	snap, err := f.reader.Snapshot()
	if err != nil {
		f.t.Fatal(err)
	}
	return snap
}

func (f *fixture) window(snap Snapshot, project, name string) Window {
	f.t.Helper()
	p, ok := snap.Project(project)
	if !ok {
		f.t.Fatalf("project %s missing", project)
	}
	w, ok := p.Window(name)
	if !ok {
		f.t.Fatalf("window %s/%s missing", project, name)
	}
	return w
}

func TestWindowStates(t *testing.T) {
	f := newFixture(t)
	f.project("alpha",
		store.Window{Name: "asker", Harness: "claude", SessionID: "s-asker"},
		store.Window{Name: "busy", Harness: "claude", SessionID: "s-busy"},
		store.Window{Name: "quiet", Harness: "claude", SessionID: "s-quiet"},
		store.Window{Name: "fresh", Harness: "claude", SessionID: "s-none"},
		store.Window{Name: "gone", Harness: "claude", SessionID: "s-gone"},
	)
	f.item("alpha", "asker")
	f.transcript(filepath.Join(f.dir, "claude", "projects", "-work", "s-busy.jsonl"), 10*time.Second)
	f.transcript(filepath.Join(f.dir, "claude", "projects", "-other", "s-quiet.jsonl"), 12*time.Minute)
	gone := f.window(f.snapshot(), "alpha", "gone")
	exec.Command("tmux", "-L", f.tmux.Socket, "kill-window", "-t", gone.TmuxTarget).Run()

	snap := f.snapshot()
	now := time.Now()
	cases := []struct {
		window string
		state  State
		label  string
	}{
		{"asker", Asking, "asks you"},
		{"busy", Running, "running"},
		{"quiet", Idle, "idle 12m"},
		{"fresh", Running, "running"},
		{"gone", Dead, "dead"},
		{"manager", Running, "running"},
	}
	for _, c := range cases {
		t.Run(c.window, func(t *testing.T) {
			w := f.window(snap, "alpha", c.window)
			if got := w.State(now); got != c.state {
				t.Errorf("state = %v, want %v", got, c.state)
			}
			if got := w.Label(now); got != c.label {
				t.Errorf("label = %q, want %q", got, c.label)
			}
		})
	}
	p, _ := snap.Project("alpha")
	if p.Open != 1 || p.Dead() != 1 {
		t.Errorf("project open=%d dead=%d, want 1 and 1", p.Open, p.Dead())
	}
}

func TestItemsWithoutSourceCountAgainstManager(t *testing.T) {
	f := newFixture(t)
	f.project("alpha", store.Window{Name: "w1", Harness: "claude"})
	f.item("alpha", "")
	f.item("alpha", "")
	f.item("alpha", "w1")
	snap := f.snapshot()
	if got := f.window(snap, "alpha", "manager").Open; got != 2 {
		t.Errorf("manager open = %d, want 2", got)
	}
	if got := f.window(snap, "alpha", "w1").Open; got != 1 {
		t.Errorf("w1 open = %d, want 1", got)
	}
}

func TestCodexTranscripts(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fixture, rollout string)
	}{
		{"state db rollout path", func(f *fixture, rollout string) {
			f.transcript(rollout, 30*time.Minute)
			os.MkdirAll(filepath.Join(f.dir, "codex"), 0o755)
			db, err := sql.Open("sqlite", filepath.Join(f.dir, "codex", "state_5.sqlite"))
			if err != nil {
				f.t.Fatal(err)
			}
			defer db.Close()
			if _, err := db.Exec(`CREATE TABLE threads (id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL)`); err != nil {
				f.t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO threads VALUES ('t-1', ?)`, rollout); err != nil {
				f.t.Fatal(err)
			}
		}},
		{"sessions glob", func(f *fixture, _ string) {
			f.transcript(filepath.Join(f.dir, "codex", "sessions", "2026", "10", "13", "rollout-2026-10-13T09-00-00-t-1.jsonl"), 30*time.Minute)
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFixture(t)
			f.project("alpha", store.Window{Name: "cx", Harness: "codex", SessionID: "t-1"})
			c.setup(f, filepath.Join(f.dir, "elsewhere", "rollout.jsonl"))
			if got := f.window(f.snapshot(), "alpha", "cx").Label(time.Now()); got != "idle 30m" {
				t.Errorf("label = %q, want idle 30m", got)
			}
		})
	}
}

func TestSnapshotHangsDiscoveredSessionsOnTheirWindowsAndListsTheRest(t *testing.T) {
	f := newFixture(t)
	f.project("alpha")
	adopted, err := f.tmux.NewWindow("scratch", "kept", "", f.dir, nil, []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	stray, err := f.tmux.NewWindow("scratch", "loose", "", f.dir, nil, []string{"sleep", "600"})
	if err != nil {
		t.Fatal(err)
	}
	w := store.Window{Project: "alpha", Name: "kept", Role: store.RoleWorker, Harness: "claude"}
	tag := func(w store.Window) (string, error) { return adopted, f.tmux.Tag(adopted, strconv.FormatInt(w.ID, 10)) }
	if err := f.store.AddWindow(w, tag); err != nil {
		t.Fatal(err)
	}
	showDialog(t, f.tmux.Socket, stray, "claude-bash")
	since := time.Now().Add(-time.Hour)
	f.reader.Discover = func() ([]discover.Session, error) {
		return []discover.Session{
			{Harness: "claude", ID: "alpha-mgr", Title: "plan alpha", Status: discover.Running},
			{Harness: "claude", ID: "s-kept", WindowID: adopted, Title: "kept work", Status: discover.YourTurn, Since: since},
			{Harness: "claude", ID: "s-loose", WindowID: stray, Pane: stray, Title: "loose work", Status: discover.Running},
			{Harness: "codex", ID: "s-calm", Title: "calm work", Status: discover.Idle, Since: since},
		}, nil
	}
	var snap Snapshot
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if snap = f.snapshot(); len(snap.Sessions) == 2 && snap.Sessions[0].Permission != nil {
			break
		}
	}
	if d := f.window(snap, "alpha", "manager").Discovered; d == nil || d.Title != "plan alpha" {
		t.Errorf("manager discovered = %+v", d)
	}
	kept := f.window(snap, "alpha", "kept")
	if kept.Discovered == nil || kept.Discovered.Title != "kept work" || !kept.LastActive.Equal(since) {
		t.Errorf("kept = %+v", kept)
	}
	var got []string
	for _, s := range snap.Sessions {
		got = append(got, s.Title+": "+s.Label(since.Add(90*time.Minute)))
	}
	want := []string{"loose work: needs permission · Bash: rm -rf build/ && mkdir build", "calm work: idle 1h"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("sessions = %q, want %q", got, want)
	}
}

func TestDiscoveredStatusDecidesRunningOrIdle(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name   string
		status discover.Status
		quiet  time.Duration
		want   string
	}{
		{"turn ended just now", discover.YourTurn, 5 * time.Second, "idle 5s"},
		{"waiting on a prompt", discover.NeedsYou, 30 * time.Second, "idle 30s"},
		{"working with a quiet transcript", discover.Running, 10 * time.Minute, "running"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := Window{Alive: true, LastActive: now.Add(-c.quiet), Discovered: &discover.Session{Status: c.status}}
			if got := w.Label(now); got != c.want {
				t.Fatalf("label = %q, want %q", got, c.want)
			}
		})
	}
}

func TestDiscoveredSessionLabels(t *testing.T) {
	now := time.Now()
	cases := []struct {
		s    discover.Session
		want string
	}{
		{discover.Session{Status: discover.Idle, Since: now.Add(-12 * time.Minute)}, "idle 12m"},
		{discover.Session{Status: discover.NeedsYou, WaitingFor: "permission"}, "waiting on you · permission"},
		{discover.Session{Status: discover.NeedsYou}, "waiting on you"},
		{discover.Session{Status: discover.Running}, "running"},
	}
	for _, c := range cases {
		if got := (Session{Session: c.s}).Label(now); got != c.want {
			t.Errorf("label = %q, want %q", got, c.want)
		}
	}
}
