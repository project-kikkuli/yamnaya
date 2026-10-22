package core

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/project-kikkuli/yamnaya/internal/proc"
)

// fakeLaunches starts fake Claude sessions in the world instead of tmux panes, or fails every start while fail is set.
type fakeLaunches struct {
	sends
	w      *world
	fail   error
	starts []NewLaunch
	forgot []string
}

func (l *fakeLaunches) Forget(ref string) (string, error) {
	l.forgot = append(l.forgot, ref)
	l.w.stop(ref)
	return "forgot " + ref, nil
}

func (l *fakeLaunches) Start(n NewLaunch) (string, error) {
	l.starts = append(l.starts, n)
	if l.fail != nil {
		return "", l.fail
	}
	l.w.spawn(n.Session, "")
	return "started " + n.Session, nil
}

type orgWorld struct {
	*world
	launches *fakeLaunches
	clock    time.Time
}

func newOrgWorld(t *testing.T) *orgWorld {
	t.Helper()
	w := &orgWorld{world: newWorld(t), clock: time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)}
	w.launches = &fakeLaunches{w: w.world}
	w.core.Launches = w.launches
	w.core.StateDir = t.TempDir()
	w.core.Now = func() time.Time { return w.clock }
	w.claude.inbox = map[string]error{}
	return w
}

// run starts a fake Claude session id, spawned by parent when it is set, that takes messages in its own inbox.
func (w *world) spawn(id, parent string) {
	pid := 1000 + len(w.procs)
	tty := fmt.Sprintf("/dev/ttys%d", pid)
	w.procs = append(w.procs, proc.Proc{PID: pid, Args: []string{"claude"}, TTY: tty})
	w.claude.sessions[pid] = Session{ID: id, Status: Idle, Title: "work " + id}
	if w.claude.inbox != nil {
		w.claude.inbox[id] = nil
	}
	if parent != "" {
		w.core.Store.Link(id, parent)
	}
}

func (w *world) stop(id string) {
	w.procs = slices.DeleteFunc(w.procs, func(p proc.Proc) bool { return w.claude.sessions[p.PID].ID == id })
}

func (w *world) set(id string, edit func(*Session)) {
	for pid, s := range w.claude.sessions {
		if s.ID == id {
			edit(&s)
			w.claude.sessions[pid] = s
		}
	}
}

func (w *world) handedTo(id string) []string {
	var out []string
	for _, h := range w.claude.handed {
		if text, ok := strings.CutPrefix(h, id+": "); ok {
			out = append(out, text)
		}
	}
	return out
}

func (w *orgWorld) goal(t *testing.T, ask, repo string) (Goal, Manager) {
	t.Helper()
	g, m, err := w.core.NewGoal(ask, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	return g, m
}

func (w *orgWorld) chief(t *testing.T, name string, over ...string) Manager {
	t.Helper()
	m, err := w.core.NewChief(name, "", "/repo/august", over)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (w *orgWorld) ask(t *testing.T, session, question string) Item {
	t.Helper()
	it, err := w.core.AddItem(Item{Session: session, Harness: "claude", Kind: KindDecision, Question: question})
	if err != nil {
		t.Fatal(err)
	}
	return it
}

func (w *orgWorld) supervise(t *testing.T) Board {
	t.Helper()
	b := w.board(t)
	if err := w.core.Supervise(b); err != nil {
		t.Fatal(err)
	}
	return w.board(t)
}

func ownerItems(b Board) []Item {
	var out []Item
	for _, n := range b.Needs {
		if n.Item != nil {
			out = append(out, n.Asks...)
			if len(n.Asks) == 0 {
				out = append(out, *n.Item)
			}
		}
	}
	return out
}

func goalRows(b Board) []string {
	var out []string
	for _, n := range b.Needs {
		if n.Goal != nil {
			out = append(out, n.Goal.ID+" "+n.Goal.State)
		}
	}
	return out
}

func TestAgentAsksUnderAManagerReachTheManagerAndNeverTheOwner(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "make the importer handle CSV", "/repo/august")
	for i := range 20 {
		agent := fmt.Sprintf("agent-%d", i)
		parent := m.Session
		if i%2 == 1 {
			parent = fmt.Sprintf("agent-%d", i-1)
		}
		w.spawn(agent, parent)
		w.ask(t, agent, fmt.Sprintf("question %d?", i))
	}
	b := w.board(t)
	if got := ownerItems(b); len(got) != 0 {
		t.Fatalf("owner queue holds %d agent asks, want none: %+v", len(got), got)
	}
	handed := w.handedTo(m.Session)
	if len(handed) != 20 {
		t.Fatalf("manager got %d asks, want 20: %q", len(handed), handed)
	}
	for i, text := range handed {
		if !strings.Contains(text, fmt.Sprintf("question %d?", i)) || !strings.Contains(text, "ym item answer") {
			t.Fatalf("ask %d reached the manager as %q", i, text)
		}
	}
}

func TestManagerAnswersReachTheAgentThatAsked(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	w.spawn("agent", m.Session)
	it := w.ask(t, "agent", "CSV or TSV?")
	if _, _, err := w.core.Answer(w.board(t), it.Key(), "CSV"); err != nil {
		t.Fatal(err)
	}
	if got := w.handedTo("agent"); len(got) != 1 || !strings.Contains(got[0], "CSV") {
		t.Fatalf("agent got %q, want the manager's answer", got)
	}
}

func TestEscalationClimbsNestedChiefsAndOnlyTheTopReachesTheOwner(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
	lower := w.chief(t, "auth", m.ID)
	upper := w.chief(t, "platform", lower.ID)
	w.spawn("agent", m.Session)
	for _, tc := range []struct {
		from, wantAt string
		owner        bool
	}{
		{from: "agent", wantAt: m.Session},
		{from: m.Session, wantAt: lower.Session},
		{from: lower.Session, wantAt: upper.Session},
		{from: upper.Session, owner: true},
	} {
		t.Run(tc.from, func(t *testing.T) {
			before := len(w.claude.handed)
			it := w.ask(t, tc.from, "escalate from "+tc.from+"?")
			b := w.board(t)
			inQueue := slices.ContainsFunc(ownerItems(b), func(o Item) bool { return o.Key() == it.Key() })
			if inQueue != tc.owner {
				t.Fatalf("in owner queue = %v, want %v", inQueue, tc.owner)
			}
			delivered := w.claude.handed[before:]
			if tc.owner {
				if len(delivered) != 0 {
					t.Fatalf("top-level ask also went to %q", delivered)
				}
				return
			}
			if len(delivered) != 1 || !strings.HasPrefix(delivered[0], tc.wantAt+": ") {
				t.Fatalf("delivered %q, want one message to %s", delivered, tc.wantAt)
			}
		})
	}
}

func TestAStuckChiefIsPassedOverSoWorkNeverWaitsOnIt(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "rotate the signing keys", "/repo/auth-plane")
	chief := w.chief(t, "auth", m.ID)
	chief.Failures = stuckFailures
	if err := w.core.Store.SaveManager(chief); err != nil {
		t.Fatal(err)
	}
	it := w.ask(t, m.Session, "may I rotate in prod?")
	if !slices.ContainsFunc(ownerItems(w.board(t)), func(o Item) bool { return o.Key() == it.Key() }) {
		t.Fatalf("escalation waited on a stuck chief instead of reaching the owner")
	}
}

func TestADeadManagerIsRelaunchedAndKeepsItsIdentity(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	w.spawn("agent", m.Session)
	w.clock = w.clock.Add(StartGrace + time.Minute)
	w.supervise(t)
	w.stop(m.Session)
	w.supervise(t)
	if len(w.launches.starts) != 2 {
		t.Fatalf("launches = %d, want the first and one relaunch", len(w.launches.starts))
	}
	again := w.launches.starts[1]
	if !strings.Contains(again.Prompt, g.Ask) || !strings.Contains(again.Prompt, g.BriefPath) || !strings.Contains(again.Prompt, "relaunched") {
		t.Fatalf("relaunch prompt lacks the goal, brief or resume note:\n%s", again.Prompt)
	}
	now, _ := w.core.Store.Manager(m.ID)
	if now.Session == m.Session || now.Session != again.Session {
		t.Fatalf("manager session %s after relaunch, want the new %s", now.Session, again.Session)
	}
	w.ask(t, "agent", "still mine?")
	if got := w.handedTo(now.Session); len(got) != 1 {
		t.Fatalf("an agent of the old session asked; the relaunched manager got %q", got)
	}
}

func TestAManagerThatCannotLaunchBacksOffAndShowsOneStuckRow(t *testing.T) {
	w := newOrgWorld(t)
	w.launches.fail = errors.New("claude not found on PATH")
	g, _ := w.goal(t, "ship the importer", "/repo/august")
	for range 3 * 60 * 6 {
		w.clock = w.clock.Add(10 * time.Second)
		b := w.board(t)
		if err := w.core.Supervise(b); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(w.launches.starts); n > 10 {
		t.Fatalf("%d launches in three hours of a failing launch; want backoff, not a spin", n)
	}
	b := w.board(t)
	if rows := goalRows(b); !slices.Equal(rows, []string{g.ID + " stuck"}) {
		t.Fatalf("owner queue goal rows %q, want one stuck row", rows)
	}
	got, _ := w.core.Store.Goal(g.ID)
	if !strings.Contains(got.Evidence, "claude not found on PATH") {
		t.Fatalf("stuck row evidence %q lacks the launch error", got.Evidence)
	}
}

func TestAQuietManagerIsNudgedThenShowsStuckUnlessItsWorkMoves(t *testing.T) {
	for _, tc := range []struct {
		name      string
		keepBusy  bool
		askOwner  bool
		wantNudge int
		wantStuck bool
	}{
		{name: "silent", wantNudge: Nudges, wantStuck: true},
		{name: "agent working", keepBusy: true},
		{name: "waiting on the owner", askOwner: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			w.spawn("agent", m.Session)
			if tc.keepBusy {
				w.set("agent", func(s *Session) { s.Status = Busy })
			}
			if tc.askOwner {
				w.ask(t, m.Session, "may I merge in august?")
			}
			for range 5 {
				w.clock = w.clock.Add(QuietAfter + time.Minute)
				w.supervise(t)
			}
			nudges := 0
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, "nothing has moved") {
					nudges++
				}
			}
			if nudges != tc.wantNudge {
				t.Fatalf("nudged %d times, want %d", nudges, tc.wantNudge)
			}
			got, _ := w.core.Store.Goal(g.ID)
			if (got.State == GoalStuck) != tc.wantStuck {
				t.Fatalf("goal %s (%s), want stuck %v", got.State, got.Evidence, tc.wantStuck)
			}
			if tc.wantStuck && len(goalRows(w.board(t))) != 1 {
				t.Fatalf("stuck goal rows %q, want one", goalRows(w.board(t)))
			}
		})
	}
}

func TestGreenPullRequestsGoToTheOwnerOnlyWhereNoManagerMayMerge(t *testing.T) {
	for _, tc := range []struct {
		repo         string
		managed      bool
		wantQueue    bool
		wantNotified bool
	}{
		{repo: "august", managed: true, wantQueue: true},
		{repo: "auth-plane", managed: true, wantNotified: true},
		{repo: "auth-plane", managed: false, wantQueue: true},
		{repo: "august", managed: false, wantQueue: true},
	} {
		t.Run(fmt.Sprintf("%s managed=%v", tc.repo, tc.managed), func(t *testing.T) {
			w := newOrgWorld(t)
			_, m := w.goal(t, "ship it", "/repo/"+tc.repo)
			parent := ""
			if tc.managed {
				parent = m.Session
			}
			url := "https://github.com/acme/" + tc.repo + "/pull/7"
			w.spawn("agent", parent)
			w.set("agent", func(s *Session) { s.PRs = []PR{{URL: url, Repo: "acme/" + tc.repo, Number: 7}} })
			green := PRStatus{State: "OPEN", Passed: 4, Fetched: w.clock, Merge: PRMergeFacts{Author: "mira", Viewer: "mira", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
			if err := w.core.Store.SavePRStatus(url, green); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				w.supervise(t)
			}
			b := w.board(t)
			queued := slices.ContainsFunc(b.Needs, func(n Need) bool { return n.PR != nil && n.PR.URL == url })
			notices := 0
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, url) {
					notices++
				}
			}
			if queued != tc.wantQueue || (notices == 1) != tc.wantNotified || notices > 1 {
				t.Fatalf("in owner queue %v, manager told %d times; want queue %v, told %v", queued, notices, tc.wantQueue, tc.wantNotified)
			}
		})
	}
}

func TestASessionTheOwnerStartedShowsOneWaitingRowForAllItsAsks(t *testing.T) {
	w := newOrgWorld(t)
	w.goal(t, "unrelated goal", "/repo/august")
	w.spawn("mine", "")
	for i := range 10 {
		w.ask(t, "mine", fmt.Sprintf("ask %d?", i))
	}
	b := w.board(t)
	var rows []Need
	for _, n := range b.Needs {
		if n.Item != nil && n.Item.Session == "mine" {
			rows = append(rows, n)
		}
	}
	if len(rows) != 1 || len(rows[0].Asks) != 10 || rows[0].Item.Question != "ask 0?" {
		t.Fatalf("owner-started session shows %d rows (%+v), want one row holding all 10 asks, oldest first", len(rows), rows)
	}
	if len(w.launches.starts) != 1 {
		t.Fatalf("an owner-started session got a manager: %d launches", len(w.launches.starts))
	}
}

func TestAFinishedGoalShowsOnceUntilCleared(t *testing.T) {
	w := newOrgWorld(t)
	g, m := w.goal(t, "ship the importer", "/repo/august")
	if _, _, err := w.core.FinishGoal(m.Session, "importer shipped", nil); err != nil {
		t.Fatal(err)
	}
	w.supervise(t)
	if _, err := w.core.RecordVerdict(w.launches.starts[len(w.launches.starts)-1].Session, g.ID, VerdictPass, nil); err != nil {
		t.Fatal(err)
	}
	if rows := goalRows(w.board(t)); !slices.Equal(rows, []string{g.ID + " done"}) {
		t.Fatalf("goal rows %q, want the done goal once", rows)
	}
	if err := w.core.AckGoal(g.ID); err != nil {
		t.Fatal(err)
	}
	b := w.board(t)
	if rows := goalRows(b); len(rows) != 0 || len(b.Tree) != 0 {
		t.Fatalf("cleared goal still shows: rows %q, tree %d", rows, len(b.Tree))
	}
}

func TestSteeringAChiefReachesItAloneOrItsWholeSubtree(t *testing.T) {
	for _, tc := range []struct {
		subtree bool
		want    int
	}{{false, 1}, {true, 3}} {
		t.Run(fmt.Sprint(tc.subtree), func(t *testing.T) {
			w := newOrgWorld(t)
			_, a := w.goal(t, "goal a", "/repo/august")
			_, b := w.goal(t, "goal b", "/repo/august")
			chief := w.chief(t, "lead", a.ID, b.ID)
			n, err := w.core.Steer(w.board(t), []string{chief.ID}, tc.subtree, true, "focus on latency")
			if err != nil || n != tc.want {
				t.Fatalf("steered %d (%v), want %d", n, err, tc.want)
			}
		})
	}
}

func TestAsksWaitingOnAManagerThatCanNoLongerAnswerClimbToTheNextOneUp(t *testing.T) {
	for _, tc := range []struct {
		name    string
		strand  func(w *orgWorld, m Manager, g Goal)
		toChief bool
	}{
		{name: "goal dropped", strand: func(w *orgWorld, m Manager, g Goal) { w.core.DropGoal(g.ID) }, toChief: true},
		{name: "manager stuck", strand: func(w *orgWorld, m Manager, g Goal) {
			m, _ = w.core.Store.Manager(m.ID)
			m.Failures = stuckFailures
			w.core.Store.SaveManager(m)
		}, toChief: true},
		{name: "chief retired too", strand: func(w *orgWorld, m Manager, g Goal) {
			w.core.DropGoal(g.ID)
			chief, _ := w.core.Store.Manager(m.ParentID)
			w.core.DropGoal(chief.ID)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			chief := w.chief(t, "lead", m.ID)
			top := w.chief(t, "platform", chief.ID)
			m, _ = w.core.Store.Manager(m.ID)
			w.spawn("agent", m.Session)
			it := w.ask(t, "agent", "CSV or TSV?")
			tc.strand(w, m, g)
			w.supervise(t)
			got, _ := w.core.Store.Item(it.Key())
			want := chief.ID
			if !tc.toChief {
				want = top.ID
			}
			if got.Recipient != want {
				t.Fatalf("stranded ask went to %q, want %s", got.Recipient, want)
			}
			if handed := w.handedTo(map[string]string{chief.ID: chief.Session, top.ID: top.Session}[want]); !slices.ContainsFunc(handed, func(s string) bool { return strings.Contains(s, "CSV or TSV?") }) {
				t.Fatalf("%s was not told the ask: %q", want, handed)
			}
		})
	}
}

func TestMessagesQueuedForADownManagerSurviveFailedRelaunches(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	w.clock = w.clock.Add(StartGrace + time.Minute)
	w.supervise(t)
	w.stop(m.Session)
	if _, err := w.core.Steer(w.board(t), []string{m.ID}, false, true, "prefer TSV"); err != nil {
		t.Fatal(err)
	}
	w.launches.fail = errors.New("tmux server gone")
	for range 3 {
		w.clock = w.clock.Add(maxBackoff)
		w.supervise(t)
	}
	w.launches.fail = nil
	w.clock = w.clock.Add(maxBackoff)
	w.supervise(t)
	last := w.launches.starts[len(w.launches.starts)-1]
	if !strings.Contains(last.Prompt, "prefer TSV") {
		t.Fatalf("the relaunch that finally started lost the queued message:\n%s", last.Prompt)
	}
}

func TestAManagerWhoseConversationMovedKeepsItsGoal(t *testing.T) {
	w := newOrgWorld(t)
	_, m := w.goal(t, "ship the importer", "/repo/august")
	w.claude.next = map[string]string{m.Session: "moved-session"}
	w.set(m.Session, func(s *Session) { s.ID = "moved-session" })
	w.claude.inbox["moved-session"] = nil
	w.supervise(t)
	w.spawn("late-agent", "moved-session")
	w.ask(t, "late-agent", "which parser?")
	if got := w.handedTo("moved-session"); len(got) != 1 {
		t.Fatalf("manager under its new session id got %q, want the late agent's ask", got)
	}
	if _, _, err := w.core.FinishGoal("moved-session", "shipped", nil); err != nil {
		t.Fatalf("the moved manager could not finish its goal: %v", err)
	}
}
