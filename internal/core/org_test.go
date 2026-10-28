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
	// harnessOf is the harness each started session runs on, by the id its launch reported.
	harnessOf map[string]string
	forgot    []string
	launched  []Launched
	// during runs while a start is in progress, as another ym process may.
	during func()
}

func (l *fakeLaunches) End(ref, goal string) (string, error) {
	l.forgot = append(l.forgot, ref)
	l.w.stop(ref)
	return "killed " + ref, nil
}

func (l *fakeLaunches) Launched() []Launched { return l.launched }

func (l *fakeLaunches) Start(n NewLaunch) (Started, error) {
	l.starts = append(l.starts, n)
	if during := l.during; during != nil {
		l.during = nil
		during()
	}
	if l.fail != nil {
		return Started{}, l.fail
	}
	id := n.Session
	if n.Harness == "codex" {
		id = fmt.Sprintf("thread-%d", len(l.starts))
	}
	if l.harnessOf == nil {
		l.harnessOf = map[string]string{}
	}
	l.harnessOf[id] = n.Harness
	l.w.spawn(id, "")
	return Started{Session: id, Message: "started " + id}, nil
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
	w.core.Pace = &WakePace{}
	w.claude.inbox = map[string]error{}
	signalled(w)
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
	m, err := w.core.NewChief(name, "", "/repo/august", "", over)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (w *orgWorld) ask(t *testing.T, session, question string) Item {
	t.Helper()
	it, err := w.core.AddItem(Item{Rec: "r", Options: picks, Session: session, Harness: "claude", Kind: KindDecision, Question: question})
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
	w.loop(t, 2)
	handed := w.handedTo(m.Session)
	if len(handed) != 1 {
		t.Fatalf("manager got %d messages, want all 20 asks in one wake: %q", len(handed), handed)
	}
	items, err := w.core.Store.Items(ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if !strings.Contains(handed[0], it.Key()+" ask from agent "+Short(it.Session)+" to "+m.ID+": "+it.Question) {
			t.Fatalf("ask %s %q is not a line of the manager's wake:\n%s", it.Key(), it.Question, handed[0])
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
			w.loop(t, 1)
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
	w.loop(t, 1)
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
	green := func(st *PRStatus) {}
	for _, tc := range []struct {
		name         string
		repo         string
		kind         string
		status       func(*PRStatus)
		wantQueue    bool
		wantNotified bool
	}{
		{name: "goal agent, manager not cleared", repo: "august", kind: "goal", status: green, wantQueue: true},
		{name: "goal agent, manager cleared", repo: "auth-plane", kind: "goal", status: green, wantNotified: true},
		{name: "goal agent gone, manager not cleared", repo: "august", kind: "goal-gone", status: green, wantQueue: true},
		{name: "goal agent gone, manager cleared", repo: "auth-plane", kind: "goal-gone", status: green},
		{name: "owner's session, repo cleared", repo: "auth-plane", kind: "self", status: green, wantQueue: true},
		{name: "owner's session, repo not cleared", repo: "august", kind: "self", status: green, wantQueue: true},
		{name: "owner's session gone", repo: "august", kind: "self-gone", status: green, wantQueue: true},
		{name: "written elsewhere, repo cleared", repo: "auth-plane", kind: "authored", status: green, wantQueue: true},
		{name: "written elsewhere, repo not cleared", repo: "august", kind: "authored", status: green, wantQueue: true},
		{name: "written by someone else", repo: "august", kind: "self", status: func(st *PRStatus) { st.Merge.Author = "bo" }},
		{name: "red CI", repo: "august", kind: "authored", status: func(st *PRStatus) { st.Failed = 1 }},
		{name: "CI running", repo: "august", kind: "authored", status: func(st *PRStatus) { st.Pending = 1 }},
		{name: "open thread", repo: "august", kind: "authored", status: func(st *PRStatus) { st.Unresolved = 1 }},
		{name: "draft", repo: "august", kind: "authored", status: func(st *PRStatus) { st.Merge.Draft = true }},
		{name: "review required", repo: "august", kind: "authored", status: func(st *PRStatus) { st.Review = "REVIEW_REQUIRED"; st.Merge.MergeState = "BLOCKED" }},
		{name: "merged", repo: "august", kind: "authored", status: func(st *PRStatus) { st.State = "MERGED" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship it", "/repo/"+tc.repo)
			url := "https://github.com/acme/" + tc.repo + "/pull/7"
			pr := PR{URL: url, Repo: "acme/" + tc.repo, Number: 7}
			switch tc.kind {
			case "goal", "self":
				parent := ""
				if tc.kind == "goal" {
					parent = m.Session
				}
				w.spawn("agent", parent)
				w.set("agent", func(s *Session) { s.PRs = []PR{pr} })
			case "goal-gone":
				if _, _, err := w.core.RecordGoalEvent(g.ID, EventPROpened, "opened "+url, g.ID+":pr:"+url); err != nil {
					t.Fatal(err)
				}
			}
			if tc.kind != "goal" && tc.kind != "self" {
				if err := w.core.Store.SyncAuthored(map[string]string{url: "Ship it"}); err != nil {
					t.Fatal(err)
				}
			}
			st := PRStatus{State: "OPEN", Title: "Ship it", Passed: 4, Fetched: w.clock, Merge: PRMergeFacts{Author: "mira", Viewer: "mira", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
			tc.status(&st)
			if err := w.core.Store.SavePRStatus(url, st); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				w.supervise(t)
			}
			b := w.board(t)
			var rows []Need
			for _, n := range b.Needs {
				if n.PR != nil && n.PR.URL == url {
					rows = append(rows, n)
				}
			}
			notices := 0
			for _, text := range w.handedTo(m.Session) {
				if strings.Contains(text, url) {
					notices++
				}
			}
			if (len(rows) == 1) != tc.wantQueue || len(rows) > 1 || (notices == 1) != tc.wantNotified || notices > 1 {
				t.Fatalf("%d owner queue rows, manager told %d times; want queue %v, told %v", len(rows), notices, tc.wantQueue, tc.wantNotified)
			}
			if tc.wantQueue && (rows[0].PR.Repo != pr.Repo || rows[0].PR.Number != 7 || rows[0].PR.Status.Title != "Ship it") {
				t.Fatalf("queue row %+v lacks the pull request's repo, number and title", rows[0].PR)
			}
			if tc.wantQueue && strings.HasPrefix(tc.kind, "goal") != (rows[0].Manager == m.ID) {
				t.Fatalf("queue row manager %q, want the goal's manager only for its own pull request", rows[0].Manager)
			}
		})
	}
}

func TestAnAuthoredPullRequestLeavesTheQueueOnceGitHubStopsListingIt(t *testing.T) {
	w := newOrgWorld(t)
	url := "https://github.com/acme/august/pull/9"
	if err := w.core.Store.SyncAuthored(map[string]string{url: "Ship it"}); err != nil {
		t.Fatal(err)
	}
	st := PRStatus{State: "OPEN", Passed: 1, Fetched: w.clock, Merge: PRMergeFacts{Author: "mira", Viewer: "mira", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	if err := w.core.Store.SavePRStatus(url, st); err != nil {
		t.Fatal(err)
	}
	for _, listed := range []bool{true, false} {
		if !listed {
			if err := w.core.Store.SyncAuthored(nil); err != nil {
				t.Fatal(err)
			}
		}
		queued := slices.ContainsFunc(w.board(t).Needs, func(n Need) bool { return n.PR != nil && n.PR.URL == url })
		if queued != listed {
			t.Fatalf("listed by GitHub %v, queued %v", listed, queued)
		}
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

func TestADoneGoalNeverWaitsInTheQueueAndLeavesTheTreeOnceClearedOrADayOld(t *testing.T) {
	for _, tc := range []struct {
		name   string
		clear  bool
		after  time.Duration
		inTree bool
	}{
		{name: "just done", inTree: true},
		{name: "done for most of a day", after: DoneShown - time.Minute, inTree: true},
		{name: "cleared", clear: true},
		{name: "a day old", after: DoneShown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship the importer", "/repo/august")
			if _, _, err := w.core.FinishGoal(m.Session, "importer shipped", nil); err != nil {
				t.Fatal(err)
			}
			w.supervise(t)
			if _, err := w.core.RecordVerdict(w.launches.starts[len(w.launches.starts)-1].Session, g.ID, VerdictPass, nil); err != nil {
				t.Fatal(err)
			}
			if tc.clear {
				if err := w.core.AckGoal(g.ID); err != nil {
					t.Fatal(err)
				}
			}
			w.clock = w.clock.Add(tc.after)
			b := w.board(t)
			n, inTree := b.Node(m.ID)
			if rows := goalRows(b); len(rows) != 0 || inTree != tc.inTree || inTree && n.Standing != StandDone {
				t.Fatalf("queue rows %q, on the tree %v standing %q; want no queue row and on the tree %v as done", rows, inTree, n.Standing, tc.inTree)
			}
		})
	}
}

func TestASettledDutyLeavesTheOwnersQueueOnItsOwn(t *testing.T) {
	const url = "https://github.com/acme/august/pull/7"
	green := PRStatus{State: "OPEN", Passed: 4, Merge: PRMergeFacts{Author: "mira", Viewer: "mira", OID: "abc", Mergeable: "MERGEABLE", MergeState: "CLEAN"}}
	stuck := func(w *orgWorld, t *testing.T, edit func(*Goal)) {
		t.Helper()
		g, err := w.core.Store.Goal("goal1")
		if err != nil {
			t.Fatal(err)
		}
		edit(&g)
		if err := w.core.Store.SaveGoal(g); err != nil {
			t.Fatal(err)
		}
	}
	isGoal := func(n Need) bool { return n.Goal != nil }
	for _, tc := range []struct {
		name          string
		raise, settle func(*orgWorld, *testing.T)
		is            func(Need) bool
	}{
		{
			name: "stuck goal that moves again",
			raise: func(w *orgWorld, t *testing.T) {
				stuck(w, t, func(g *Goal) { g.State, g.Evidence = GoalStuck, "manager down" })
			},
			settle: func(w *orgWorld, t *testing.T) { stuck(w, t, func(g *Goal) { g.State, g.Evidence = GoalActive, "" }) },
			is:     isGoal,
		},
		{
			name: "stuck goal that passes verification",
			raise: func(w *orgWorld, t *testing.T) {
				stuck(w, t, func(g *Goal) { g.State, g.Evidence = GoalStuck, "manager down" })
			},
			settle: func(w *orgWorld, t *testing.T) {
				stuck(w, t, func(g *Goal) { g.State, g.Evidence, g.DoneAt = GoalDone, "", w.clock })
			},
			is: isGoal,
		},
		{
			name:  "ask from a session the owner started, answered",
			raise: func(w *orgWorld, t *testing.T) { w.spawn("mine", ""); w.ask(t, "mine", "which file?") },
			settle: func(w *orgWorld, t *testing.T) {
				if _, _, err := w.core.Answer(w.board(t), ownerItems(w.board(t))[0].Key(), "csv.go"); err != nil {
					t.Fatal(err)
				}
			},
			is: func(n Need) bool { return n.Item != nil },
		},
		{
			name:  "ask from a session the owner started, dismissed",
			raise: func(w *orgWorld, t *testing.T) { w.spawn("mine", ""); w.ask(t, "mine", "which file?") },
			settle: func(w *orgWorld, t *testing.T) {
				b := w.board(t)
				if _, _, err := w.core.Dismiss(b, ownerItems(b)[0].Key(), "no longer needed"); err != nil {
					t.Fatal(err)
				}
			},
			is: func(n Need) bool { return n.Item != nil },
		},
		{
			name: "green pull request, closed",
			raise: func(w *orgWorld, t *testing.T) {
				w.spawn("mine", "")
				w.pr(t, "mine", url, green)
			},
			settle: func(w *orgWorld, t *testing.T) {
				closed := green
				closed.State = "CLOSED"
				w.pr(t, "mine", url, closed)
			},
			is: func(n Need) bool { return n.PR != nil },
		},
		{
			name: "document discussion, resolved",
			raise: func(w *orgWorld, t *testing.T) {
				a := DocAsk{Key: "notion:discussion", Page: "other-page", Version: "comment1", Text: "Approve the copy?"}
				if err := w.core.Store.SyncDocPage("notion", a.Page, "edit1", []DocAsk{a}); err != nil {
					t.Fatal(err)
				}
				w.route(t)
			},
			settle: func(w *orgWorld, t *testing.T) {
				if err := w.core.Store.SyncDocPage("notion", "other-page", "edit2", nil); err != nil {
					t.Fatal(err)
				}
			},
			is: func(n Need) bool { return n.Doc != nil },
		},
		{
			name: "permission prompt, resolved",
			raise: func(w *orgWorld, t *testing.T) {
				w.spawn("mine", "")
				w.set("mine", func(s *Session) { s.Prompt = &Prompt{Tool: "Bash"} })
			},
			settle: func(w *orgWorld, t *testing.T) { w.set("mine", func(s *Session) { s.Prompt = nil }) },
			is:     func(n Need) bool { return n.Session != nil && n.Session.Prompt != nil },
		},
		{
			name: "green pull request, merged",
			raise: func(w *orgWorld, t *testing.T) {
				w.spawn("mine", "")
				w.pr(t, "mine", url, green)
			},
			settle: func(w *orgWorld, t *testing.T) {
				merged := green
				merged.State = "MERGED"
				w.pr(t, "mine", url, merged)
			},
			is: func(n Need) bool { return n.PR != nil },
		},
		{
			name: "review request, withdrawn",
			raise: func(w *orgWorld, t *testing.T) {
				if err := w.core.Store.SyncReviews([]ReviewRequest{{URL: url, Updated: "1", Title: "Read CSV", Author: "mira"}}); err != nil {
					t.Fatal(err)
				}
			},
			settle: func(w *orgWorld, t *testing.T) {
				if err := w.core.Store.SyncReviews(nil); err != nil {
					t.Fatal(err)
				}
			},
			is: func(n Need) bool { return n.Review != nil && n.Review.Title == "Read CSV" && n.Review.Author == "mira" },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.goal(t, "ship the importer", "/repo/august")
			tc.raise(w, t)
			if !slices.ContainsFunc(w.board(t).Needs, tc.is) {
				t.Fatalf("raised duty is not in the queue: %+v", w.board(t).Needs)
			}
			tc.settle(w, t)
			w.clock = w.clock.Add(time.Second)
			if b := w.board(t); slices.ContainsFunc(b.Needs, tc.is) {
				t.Fatalf("settled duty still in the queue: %+v", b.Needs)
			}
		})
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
		{name: "goal killed", strand: func(w *orgWorld, m Manager, g Goal) { w.core.KillGoal(w.board(t), g.ID) }, toChief: true},
		{name: "manager stuck", strand: func(w *orgWorld, m Manager, g Goal) {
			m, _ = w.core.Store.Manager(m.ID)
			m.Failures = stuckFailures
			w.core.Store.SaveManager(m)
		}, toChief: true},
		{name: "chief retired too", strand: func(w *orgWorld, m Manager, g Goal) {
			w.core.KillGoal(w.board(t), g.ID)
			chief, _ := w.core.Store.Manager(m.ParentID)
			w.core.KillGoal(w.board(t), chief.ID)
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
	w.loop(t, 1)
	if got := w.handedTo("moved-session"); len(got) != 1 {
		t.Fatalf("manager under its new session id got %q, want the late agent's ask", got)
	}
	if _, _, err := w.core.FinishGoal("moved-session", "shipped", nil); err != nil {
		t.Fatalf("the moved manager could not finish its goal: %v", err)
	}
}

func TestAManagerWhosePaneTmuxCannotReportIsNotTakenForDead(t *testing.T) {
	for state, alive := range map[string]bool{"running": true, LaunchUnknown: true, "gone": false, "exited 0": false} {
		b := Board{Launched: []Launched{{Session: "mgr-session", State: state}}}
		if got := b.Alive(Manager{Session: "mgr-session"}); got != alive {
			t.Errorf("launch %q: alive %v, want %v", state, got, alive)
		}
	}
}
