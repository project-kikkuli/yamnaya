package core

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

type fakeHarness struct {
	name     string
	sessions map[int]Session
	cwds     map[string]string
	// next maps a session to the one it continued in.
	next map[string]string
	// inbox is what each session's own inbox does with a message; a session missing from it has none.
	inbox  map[string]error
	handed []string
	// during runs while a message is on its way to a session's inbox.
	during func()
}

var errNoInbox = errors.New("no inbox")

func (f *fakeHarness) Successor(id, near string) string { return f.next[id] }

func (f *fakeHarness) Message(s Session, text string) error {
	err, ok := f.inbox[s.ID]
	if !ok {
		return errNoInbox
	}
	if f.during != nil {
		f.during()
	}
	if err == nil {
		f.handed = append(f.handed, s.ID+": "+text)
	}
	return err
}

func (f *fakeHarness) SessionCwd(id string) string { return f.cwds[id] }

func (f *fakeHarness) Name() string { return f.name }
func (f *fakeHarness) Owns(p proc.Proc) bool {
	return len(p.Args) > 0 && p.Args[0] == f.name
}
func (f *fakeHarness) Discover(procs []proc.Proc) []Session {
	var out []Session
	for _, p := range procs {
		s := f.sessions[p.PID]
		s.Harness, s.PID, s.TTY = f.name, p.PID, p.TTY
		out = append(out, s)
	}
	return out
}
func (f *fakeHarness) SessionID(p proc.Proc) string { return f.sessions[p.PID].ID }
func (f *fakeHarness) Resume(id string) []string    { return []string{f.name, "--resume", id} }
func (f *fakeHarness) ParsePrompt(screen string) (Prompt, bool) {
	tool, ok := strings.CutPrefix(screen, "DIALOG ")
	return Prompt{Tool: tool}, ok
}
func (f *fakeHarness) Composer(screen string) (string, bool) { return strings.CutPrefix(screen, "> ") }

type fakeHost struct {
	name    string
	ttys    map[string]string
	screens map[string]string
	outer   string
	focused []string
	typed   []string
	typeErr error
	// opens is the dialog that opens on the pane right as a paste lands in it.
	opens map[string]string
}

func (f *fakeHost) Name() string { return f.name }
func (f *fakeHost) Locate(tty string) (Handle, bool) {
	id, ok := f.ttys[tty]
	return Handle{Host: f.name, ID: id, Label: id}, ok
}
func (f *fakeHost) Focus(h Handle) (string, error) {
	f.focused = append(f.focused, h.ID)
	return f.outer, nil
}
func (f *fakeHost) Paste(h Handle, text string) error {
	if f.typeErr != nil {
		return f.typeErr
	}
	if d, ok := f.opens[h.ID]; ok {
		f.screens[h.ID] = "DIALOG " + d
		return nil
	}
	screen, _ := f.Screen(h)
	if box, ok := strings.CutPrefix(screen, "> "); ok {
		f.screens[h.ID] = "> " + box + text
	}
	return nil
}
func (f *fakeHost) Enter(h Handle) error {
	box, _ := strings.CutPrefix(f.screens[h.ID], "> ")
	f.typed = append(f.typed, h.ID+": "+box)
	f.screens[h.ID] = "> "
	return nil
}
func (f *fakeHost) Screen(h Handle) (string, error) {
	if s, ok := f.screens[h.ID]; ok {
		return s, nil
	}
	return "> ", nil
}

type world struct {
	core   *Core
	procs  []proc.Proc
	claude *fakeHarness
	codex  *fakeHarness
	tmux   *fakeHost
	gui    *fakeHost
}

func newWorld(t *testing.T) *world {
	t.Helper()
	st, err := OpenStore(filepath.Join(t.TempDir(), "ym.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	w := &world{
		claude: &fakeHarness{name: "claude", sessions: map[int]Session{}},
		codex:  &fakeHarness{name: "codex", sessions: map[int]Session{}},
		tmux:   &fakeHost{name: "tmux", ttys: map[string]string{}, screens: map[string]string{}},
		gui:    &fakeHost{name: "gui", ttys: map[string]string{}, screens: map[string]string{}},
	}
	w.core = &Core{Store: st, Harnesses: []Harness{w.claude, w.codex}, Hosts: []Host{w.tmux, w.gui}, Procs: func() ([]proc.Proc, error) { return w.procs, nil }}
	return w
}

// run adds a process; harness processes get a session with the given id and status.
func (w *world) run(pid, ppid int, tty, cmd, id string, st Status) {
	w.procs = append(w.procs, proc.Proc{PID: pid, PPID: ppid, TTY: tty, Args: []string{cmd}})
	switch cmd {
	case "claude":
		w.claude.sessions[pid] = Session{ID: id, Title: "title " + id, Status: st}
	case "codex":
		w.codex.sessions[pid] = Session{ID: id, Title: "title " + id, Status: st}
	}
}

func (w *world) board(t *testing.T) Board {
	t.Helper()
	b, err := w.core.Board()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func projectKeys(b Board) map[string][]string {
	out := map[string][]string{}
	for _, p := range b.Projects {
		for _, s := range p.Sessions {
			out[p.Key] = append(out[p.Key], s.Key())
		}
	}
	return out
}

func TestProjectsFollowProcessAncestryLinksAndMerges(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "lead", Busy)
	w.run(11, 10, "/dev/t1", "zsh", "", 0)
	w.run(12, 11, "/dev/t1", "codex", "worker", Busy)
	w.run(20, 1, "/dev/t2", "claude", "elsewhere", Idle)
	w.run(30, 1, "/dev/t3", "claude", "solo", Idle)
	w.run(40, 1, "", "claude", "headless", Busy)
	if err := w.core.Store.Link("elsewhere", "lead"); err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	got := projectKeys(b)
	if len(got) != 2 || len(got["lead"]) != 3 || got["lead"][0] != "lead" || len(got["solo"]) != 1 {
		t.Fatalf("projects = %v", got)
	}
	if p, _ := b.Project("lead"); p.Name != "title lead" {
		t.Errorf("project name = %q, want the root session's title", p.Name)
	}
	if err := w.core.Store.Merge("solo", "lead"); err != nil {
		t.Fatal(err)
	}
	if err := w.core.Store.Rename("lead", "Launch"); err != nil {
		t.Fatal(err)
	}
	b = w.board(t)
	if got := projectKeys(b); len(got) != 1 || len(got["lead"]) != 4 {
		t.Fatalf("after merge projects = %v", got)
	}
	if p, _ := b.Project("lead"); p.Name != "Launch" {
		t.Errorf("renamed project = %q", p.Name)
	}
	if b.ProjectOf("solo") != "lead" || b.ProjectOf("unknown") != "unknown" {
		t.Errorf("ProjectOf solo = %q", b.ProjectOf("solo"))
	}
}

func TestDuplicateProcessesOnOneSessionAreFlagged(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "same", Busy)
	w.run(11, 1, "/dev/t2", "claude", "same", Busy)
	w.run(20, 1, "/dev/t3", "claude", "wrapped", Idle)
	w.run(21, 20, "/dev/t3", "claude", "wrapped", Idle)
	w.tmux.ttys["/dev/t1"], w.tmux.ttys["/dev/t2"] = "%1", "%2"
	b := w.board(t)
	if s, _ := b.Session("wrapped"); len(s.Copies) != 1 || s.PID != 21 {
		t.Errorf("a launcher and its child on one terminal = %+v, want one copy, the child", s)
	}
	p, _ := b.Project("same")
	if !p.Duplicated() || len(p.Sessions) != 1 || len(p.Sessions[0].Copies) != 2 {
		t.Fatalf("project = %+v", p)
	}
	ts := w.core.Targets(b, "same", "claude")
	if len(ts) != 2 || ts[0].Handle.ID != "%1" || ts[1].Handle.ID != "%2" {
		t.Fatalf("targets = %+v", ts)
	}
}

func TestNeedsListPermissionPromptsThenItemsByBlocks(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "a", Waiting)
	w.claude.sessions[10] = Session{ID: "a", Status: Waiting, WaitingFor: "permission prompt"}
	w.run(20, 1, "/dev/t2", "codex", "b", Busy)
	w.codex.sessions[20] = Session{ID: "b", Status: Busy, CheckScreen: true}
	w.run(30, 1, "/dev/t3", "claude", "c", Busy)
	w.tmux.ttys["/dev/t1"], w.tmux.ttys["/dev/t2"], w.tmux.ttys["/dev/t3"] = "%1", "%2", "%3"
	w.tmux.screens["%1"], w.tmux.screens["%2"], w.tmux.screens["%3"] = "DIALOG Bash: ls", "DIALOG Shell: rm", "DIALOG ignored"
	for _, it := range []Item{
		{Kind: KindDecision, Question: "low", Session: "a", Blocks: 0},
		{Kind: KindChore, Question: "high", Session: "c", Blocks: 3},
		{Kind: KindDefault, Question: "gone session", Session: "ended", Blocks: 1},
	} {
		if _, err := w.core.AddItem(it); err != nil {
			t.Fatal(err)
		}
	}
	b := w.board(t)
	var got []string
	for _, n := range b.Needs {
		if n.Item != nil {
			got = append(got, n.Item.Question)
		} else {
			got = append(got, n.Session.Prompt.Tool)
		}
	}
	want := []string{"Bash: ls", "Shell: rm", "high", "low"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("needs = %v, want %v", got, want)
	}
	if len(b.Stale) != 1 || b.Stale[0].Item.Question != "gone session" {
		t.Errorf("stale = %+v, want the item whose session ended", b.Stale)
	}
	if _, ok := b.Project("ended"); !ok {
		t.Error("an open item from an ended session should keep its project on the board")
	}
}

func TestAnswerTypesIntoIdleAgentAndQueuesOtherwise(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "claude", "idle", Idle)
	w.run(20, 1, "/dev/t2", "claude", "busy", Busy)
	w.run(30, 1, "/dev/t3", "claude", "nohost", Idle)
	w.run(40, 1, "/dev/t4", "claude", "twice", Idle)
	w.run(41, 1, "/dev/t5", "claude", "twice", Idle)
	w.run(50, 1, "/dev/t6", "claude", "dialog", Idle)
	w.run(60, 1, "/dev/t7", "claude", "opens", Idle)
	for tty, id := range map[string]string{"/dev/t1": "%1", "/dev/t2": "%2", "/dev/t4": "%4", "/dev/t5": "%5", "/dev/t6": "%6", "/dev/t7": "%7"} {
		w.tmux.ttys[tty] = id
	}
	w.tmux.screens["%6"] = "DIALOG Fetch"
	w.tmux.opens = map[string]string{"%7": "Bash"}
	cases := []struct {
		session string
		want    Delivery
	}{
		{"idle", Typed},
		{"busy", Queued},
		{"nohost", Queued},
		{"twice", Queued},
		{"ended", Queued},
		{"dialog", Queued},
		{"opens", Queued},
	}
	for _, c := range cases {
		it, err := w.core.AddItem(Item{Kind: KindDecision, Question: "q " + c.session, Session: c.session})
		if err != nil {
			t.Fatal(err)
		}
		_, d, err := w.core.Answer(w.board(t), it.ID, "go ahead\nnow")
		if err != nil || d != c.want {
			t.Errorf("%s: delivery = %v, %v; want %v", c.session, d, err, c.want)
		}
	}
	if len(w.tmux.typed) != 1 || w.tmux.typed[0] != "%1: re D1: go ahead now" {
		t.Fatalf("typed = %q", w.tmux.typed)
	}
	inbox, err := w.core.Inbox("busy")
	if err != nil || len(inbox) != 1 || inbox[0].Answer != "go ahead\nnow" {
		t.Fatalf("busy inbox = %+v, %v", inbox, err)
	}
	if again, _ := w.core.Inbox("busy"); len(again) != 0 {
		t.Errorf("inbox repeats received answers: %+v", again)
	}
	if idle, _ := w.core.Inbox("idle"); len(idle) != 0 {
		t.Errorf("a typed answer stays in the inbox: %+v", idle)
	}
	w.tmux.typeErr = errors.New("pane gone")
	it, _ := w.core.AddItem(Item{Kind: KindChore, Question: "q", Session: "idle"})
	if _, d, _ := w.core.Dismiss(w.board(t), it.ID, "not needed"); d != Queued {
		t.Errorf("failed typing should queue, got %v", d)
	}
}

func TestFocusBringsOuterTerminalForwardAndFallsBackToResume(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/pane", "claude", "intmux", Idle)
	w.procs = append(w.procs, proc.Proc{PID: 19, PPID: 1, Args: []string{"/Applications/iTerm.app/Contents/MacOS/iTerm2"}})
	w.run(20, 19, "/dev/ttys012", "codex", "bare", Idle)
	w.tmux.ttys["/dev/pane"] = "%7"
	w.tmux.outer = "/dev/client"
	w.gui.ttys["/dev/client"] = "tab 3"
	b := w.board(t)
	ts := w.core.Targets(b, "intmux", "claude")
	if err := w.core.Focus(ts[0]); err != nil {
		t.Fatal(err)
	}
	if len(w.tmux.focused) != 1 || len(w.gui.focused) != 1 || w.gui.focused[0] != "tab 3" {
		t.Fatalf("tmux focused %v, gui focused %v", w.tmux.focused, w.gui.focused)
	}
	bare := w.core.Targets(b, "bare", "codex")
	if err := w.core.Focus(bare[0]); !errors.Is(err, ErrNoHost) || strings.Join(bare[0].Resume, " ") != "codex --resume bare" {
		t.Fatalf("bare target = %+v, err %v", bare, err)
	}
	if s, _ := b.Session("bare"); s.Location() != "iTerm ttys012" || !strings.Contains(bare[0].Advice(), "pid 20 on ttys012 in iTerm") {
		t.Errorf("location %q, advice %q", s.Location(), bare[0].Advice())
	}
	ended := w.core.Targets(b, "gone", "claude")
	if len(ended) != 1 || ended[0].Handle != nil || ended[0].Resume == nil {
		t.Fatalf("ended target = %+v", ended)
	}
}

func TestIdentifyWalksAncestryToTheHarness(t *testing.T) {
	w := newWorld(t)
	w.run(10, 1, "/dev/t1", "codex", "thread-1", Busy)
	w.run(11, 10, "/dev/t1", "sandbox", "", 0)
	w.run(12, 11, "/dev/t1", "sh", "", 0)
	w.run(13, 12, "/dev/t1", "ym", "", 0)
	w.run(20, 1, "", "sh", "", 0)
	c, err := w.core.Identify(13)
	if err != nil || c.Session != "thread-1" || c.Harness != "codex" || c.PID != 10 {
		t.Fatalf("Identify = %+v, %v", c, err)
	}
	if _, err := w.core.Identify(20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Identify outside a session = %v", err)
	}
}

func TestItemIDsAreNeverReusedAcrossKinds(t *testing.T) {
	w := newWorld(t)
	var ids []string
	for _, k := range []string{KindDecision, KindDefault, KindChore, KindDecision} {
		it, err := w.core.AddItem(Item{Kind: k, Question: "q", Session: "s"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "D1,V2,C3,D4" {
		t.Fatalf("ids = %v", ids)
	}
	if _, err := w.core.AddItem(Item{Kind: "other", Question: "q", Session: "s"}); err == nil {
		t.Error("unknown kind accepted")
	}
	if _, err := w.core.AddItem(Item{Kind: KindChore, Question: "q"}); err == nil {
		t.Error("item without a session accepted")
	}
	if _, err := w.core.Snooze("d1"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.core.Store.Transition("D1", ItemSnoozed, ""); err == nil {
		t.Error("snoozed a snoozed item")
	}
	it, err := w.core.Reopen("D1")
	if err != nil || it.State != ItemOpen || len(it.History) != 3 {
		t.Fatalf("reopen = %+v, %v", it, err)
	}
}

func TestPhasesAdvanceAndReplacingFollowsTheCurrentPhaseByName(t *testing.T) {
	cases := []struct {
		name    string
		next    []string
		current string
	}{
		{"current phase survives at a new position", []string{"plan", "spec", "build", "ship"}, "build"},
		{"current phase is gone", []string{"design", "launch"}, "design"},
		{"list shrinks past the old index", []string{"build"}, "build"},
	}
	for i, c := range cases {
		st := newWorld(t).core.Store
		key := fmt.Sprint("p", i)
		if p, err := st.SetPhases(key, []string{"spec", "build", "ship"}); err != nil || p.Current != 0 {
			t.Fatalf("set = %+v, %v", p, err)
		}
		st.NextPhase(key)
		p, err := st.SetPhases(key, c.next)
		if err != nil || p.Names[p.Current] != c.current {
			t.Errorf("%s: current = %d in %v, want %q", c.name, p.Current, p.Names, c.current)
		}
	}
	st := newWorld(t).core.Store
	st.SetPhases("q", []string{"a", "b"})
	st.NextPhase("q")
	if _, err := st.NextPhase("q"); err == nil {
		t.Error("advanced past the last phase")
	}
}

func TestOnlySessionsInATerminalAppAreAdoptable(t *testing.T) {
	w := newWorld(t)
	w.run(100, 1, "", "/opt/homebrew/bin/tmux", "", Idle)
	w.run(101, 100, "/dev/t1", "zsh", "", Idle)
	w.run(102, 101, "/dev/t1", "claude", "untracked-tmux", Idle)
	w.run(200, 1, "", "/Applications/iTerm.app/Contents/MacOS/iTerm2", "", Idle)
	w.run(201, 200, "/dev/t2", "zsh", "", Idle)
	w.run(202, 201, "/dev/t2", "claude", "iterm", Idle)
	w.run(300, 1, "", "/Applications/Claude.app/Contents/MacOS/Claude", "", Idle)
	w.run(302, 300, "", "claude", "desktop", Idle)
	w.run(402, 1, "/dev/t4", "claude", "tracked-tmux", Idle)
	w.tmux.ttys["/dev/t4"] = "%4"
	if _, err := w.core.Find("desktop"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a session with no terminal, as Claude Desktop runs, was found: %v", err)
	}
	for id, want := range map[string]bool{"untracked-tmux": false, "iterm": true, "tracked-tmux": false} {
		s, err := w.core.Find(id)
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Adoptable(); got != want {
			t.Errorf("%s: Adoptable = %v (%s), want %v", id, got, s.Unadoptable(), want)
		}
	}
}
