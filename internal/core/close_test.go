package core

import (
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func (w *orgWorld) closure(t *testing.T, goal string) *GoalClosure {
	t.Helper()
	o, err := w.core.LoadOrg()
	if err != nil {
		t.Fatal(err)
	}
	return o.Goals[goal].Closure
}

func (w *orgWorld) kinds(t *testing.T, goal string) []string {
	t.Helper()
	events, err := w.core.Store.GoalEvents(goal)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range events {
		out = append(out, e.Kind)
	}
	return out
}

func (w *verifyWorld) pass(t *testing.T) {
	t.Helper()
	w.done(t, "shipped")
	w.tick(t, time.Second)
	vs := w.verifiers()
	if _, err := w.core.RecordVerdict(vs[len(vs)-1], w.goal.ID, VerdictPass, []string{"checked"}); err != nil {
		t.Fatal(err)
	}
}

func TestClosingAGoalEndsItsSessionsAndRecordsWhy(t *testing.T) {
	for _, tc := range []struct {
		name     string
		as       string
		override bool
		start    func(*verifyWorld, *testing.T)
		state    string
		wants    string
	}{
		{name: "finished after a pass keeps done", as: CloseFinished, start: (*verifyWorld).pass, state: GoalDone, wants: CloseFinished},
		{name: "superseded while active drops", as: CloseSuperseded, state: GoalDropped, wants: CloseSuperseded},
		{name: "abandoned while active drops", as: CloseAbandoned, state: GoalDropped, wants: CloseAbandoned},
		{name: "abandoned while verifying drops", as: CloseAbandoned, start: func(w *verifyWorld, t *testing.T) { w.done(t, "shipped"); w.tick(t, time.Second) }, state: GoalDropped, wants: CloseAbandoned},
		{name: "superseded after a pass keeps done", as: CloseSuperseded, start: (*verifyWorld).pass, state: GoalDone, wants: CloseSuperseded},
		{name: "abandoned after a kill keeps killed", as: CloseAbandoned, start: func(w *verifyWorld, t *testing.T) {
			if _, err := w.core.KillGoal(w.board(t), w.goal.ID); err != nil {
				t.Fatal(err)
			}
		}, state: GoalKilled, wants: CloseAbandoned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.spawn("agent-1", w.manager.Session)
			it := w.ask(t, "agent-1", "which way?")
			if tc.start != nil {
				tc.start(w, t)
			}
			was, err := w.core.Store.Goal(w.goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			closed, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: w.goal.ID, As: tc.as, Why: "goal27 took it over", Author: Owner})
			if err != nil {
				t.Fatal(err)
			}
			g, err := w.core.Store.Goal(w.goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if g.State != tc.state || g.AckedAt.IsZero() || closed.Disposition != tc.wants {
				t.Fatalf("state %s acked %v disposition %q, want %s acked %q", g.State, g.AckedAt, closed.Disposition, tc.state, tc.wants)
			}
			if tc.state != GoalDropped && !g.DoneAt.Equal(was.DoneAt) {
				t.Fatalf("a close moved done_at from %v to %v", was.DoneAt, g.DoneAt)
			}
			cl := w.closure(t, g.ID)
			if cl == nil || cl.Disposition != tc.wants || cl.Reason != "goal27 took it over" || cl.Actor != "the owner" {
				t.Fatalf("closure %+v", cl)
			}
			b := w.board(t)
			if _, shown := b.Node(w.manager.ID); shown {
				t.Fatal("a closed goal stays on the tree")
			}
			for _, s := range []string{w.manager.Session, "agent-1"} {
				if _, alive := b.Session(s); alive {
					t.Fatalf("session %s outlived the close", s)
				}
			}
			after, err := w.core.Store.Item(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			if after.State != ItemDismissed || !strings.Contains(after.Reason, "goal27 took it over") {
				t.Fatalf("its open item is %s (%q), want dismissed with the reason", after.State, after.Reason)
			}
			w.supervise(t)
			if open := ownerItems(w.board(t)); slices.ContainsFunc(open, func(i Item) bool { return i.Seq == it.Seq }) {
				t.Fatal("a closed goal's item climbed to the owner")
			}
			pending, err := w.core.Store.unnotified()
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(pending, func(e GoalEvent) bool { return e.Kind == EventGoalClosed }) {
				t.Fatal("a close raises a desktop notification")
			}
			archived := w.board(t).ArchivedGoals()
			if len(archived) != 1 || archived[0].Disposition != tc.wants || archived[0].Reason != "goal27 took it over" || !archived[0].Reopens {
				t.Fatalf("archived %+v", archived)
			}
		})
	}
}

func TestACloseListsOpenPullRequestsAndLeavesThemAlone(t *testing.T) {
	w := newOrgWorld(t)
	g, _ := w.goal(t, "ship", "/repo/app")
	const open, merged = "https://github.com/o/r/pull/1", "https://github.com/o/r/pull/2"
	for url, state := range map[string]string{open: "OPEN", merged: "MERGED"} {
		if err := w.core.Store.SavePRStatus(url, PRStatus{State: state}); err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.core.Store.AddGoalEvent(GoalEvent{Goal: g.ID, Kind: EventPROpened, Key: g.ID + ":pr:" + url}); err != nil {
			t.Fatal(err)
		}
	}
	closed, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "owner stopped it"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(closed.PRs, []string{open}) || !strings.Contains(closed.String(), open) {
		t.Fatalf("close lists %v in %q, want only %s", closed.PRs, closed, open)
	}
	if cl := w.closure(t, g.ID); cl == nil || !slices.Equal(cl.PRs, []string{open}) {
		t.Fatalf("event holds %+v, want the open pull request", cl)
	}
}

func TestOnlyAChiefAboveOrTheOwnerClosesAGoal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		session func(boss, mid, stranger, mgr Manager) string
		by      string
		refused string
		actor   string
	}{
		{name: "owner at a terminal", session: func(_, _, _, _ Manager) string { return "" }, actor: "the owner"},
		{name: "direct chief", session: func(_, mid, _, _ Manager) string { return mid.Session }, actor: "chief "},
		{name: "chief two levels up", session: func(boss, _, _, _ Manager) string { return boss.Session }, actor: "chief "},
		{name: "chief of something else", session: func(_, _, stranger, _ Manager) string { return stranger.Session }, refused: "does not report"},
		{name: "the goal's own manager", session: func(_, _, _, mgr Manager) string { return mgr.Session }, refused: "ym goal done"},
		{name: "a worker, which must not count as the owner", session: func(_, _, _, _ Manager) string { return "worker" }, refused: "not a chief"},
		{name: "a session outside any goal", session: func(_, _, _, _ Manager) string { return "loose" }, refused: "not a chief"},
		{name: "a worker naming a chief", session: func(_, _, _, _ Manager) string { return "worker" }, by: "laptop/mgr9", refused: "not a chief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, mgr := w.goal(t, "ship", "/repo/app")
			mid := w.chief(t, "mid", mgr.ID)
			boss := w.chief(t, "boss", mid.ID)
			stranger := w.chief(t, "stranger")
			w.supervise(t)
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			mid, boss, stranger = o.Managers[mid.ID], o.Managers[boss.ID], o.Managers[stranger.ID]
			w.spawn("worker", mgr.Session)
			w.spawn("loose", "")
			author, err := w.core.CloseAuthor(tc.session(boss, mid, stranger, mgr), tc.by)
			if err == nil {
				_, err = w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "not needed", Author: author})
			}
			if tc.refused != "" {
				if err == nil || !errors.Is(err, ErrFinal) || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("err = %v, want a final refusal naming %q", err, tc.refused)
				}
				if now, _ := w.core.Store.Goal(g.ID); now.State != GoalActive {
					t.Fatalf("refused close left the goal %s", now.State)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cl := w.closure(t, g.ID); cl == nil || !strings.HasPrefix(cl.Actor, tc.actor) {
				t.Fatalf("closure %+v, want actor %q", cl, tc.actor)
			}
		})
	}
}

func TestAChiefAboveARetiredChiefStillReachesTheGoalAndARetiredChiefDoesNot(t *testing.T) {
	for _, tc := range []struct {
		name string
		act  func(*orgWorld, Board, Goal, string) error
	}{
		{"close", func(w *orgWorld, b Board, g Goal, author string) error {
			_, err := w.core.CloseGoal(b, CloseOrder{Ref: g.ID, As: CloseSuperseded, Why: "folded", Author: author})
			return err
		}},
		{"steer", func(w *orgWorld, b Board, g Goal, author string) error {
			_, err := w.core.SteerContext(b, g.ID, "new plan", author)
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, mgr := w.goal(t, "ship", "/repo/app")
			mid := w.chief(t, "mid", mgr.ID)
			boss := w.chief(t, "boss", mid.ID)
			w.supervise(t)
			if _, err := w.core.KillGoal(w.board(t), mid.ID); err != nil {
				t.Fatal(err)
			}
			if err := tc.act(w, w.board(t), g, mid.ID); err == nil || !errors.Is(err, ErrFinal) {
				t.Fatalf("the retired chief acted: %v", err)
			}
			if err := tc.act(w, w.board(t), g, boss.ID); err != nil {
				t.Fatalf("the chief above the retired one was refused: %v", err)
			}
		})
	}
}

// closeRunner runs a relayed ym goal close on w the way the CLI parses it, as a shell no harness runs.
func closeRunner(t *testing.T, w *orgWorld) func(args ...string) (string, error) {
	return func(args ...string) (string, error) {
		if strings.Join(args[:2], " ") != "goal close" {
			t.Fatalf("ran %q, want a goal close", args)
		}
		order := CloseOrder{Ref: args[2]}
		var by string
		for i := 3; i < len(args); i++ {
			switch args[i] {
			case "--as":
				i++
				order.As = args[i]
			case "--why":
				i++
				order.Why = args[i]
			case "--by":
				i++
				by = args[i]
			case "--override-verifier":
				order.Override = true
			}
		}
		var err error
		if order.Author, err = w.core.CloseAuthor("", by); err != nil {
			return "", err
		}
		closed, err := w.core.CloseGoal(w.board(t), order)
		return closed.String(), err
	}
}

func TestAChiefOnAnotherHostClosesAGoalDirectlyOrThroughAVouch(t *testing.T) {
	for _, tc := range []struct {
		name string
		// topOn wires which chief stands above the goal: a direct parent, or a chief on a third host above the chief between.
		between  bool
		stranger bool
		closes   bool
	}{
		{name: "direct parent on another host", closes: true},
		{name: "two hosts up, vouched by the chief between", between: true, closes: true},
		{name: "a stranger two hosts away is told no", between: true, stranger: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			top := f.c.chief(t, "org")
			other := f.c.chief(t, "elsewhere")
			g, mgr := f.b.goal(t, "ship the fix", "/repo/app")
			if tc.between {
				mid := f.a.chief(t, "platform")
				if err := f.a.core.Attach(mid.ID, RemoteKey("c", top.ID)); err != nil {
					t.Fatal(err)
				}
				if err := f.b.core.Attach(mgr.ID, RemoteKey("a", mid.ID)); err != nil {
					t.Fatal(err)
				}
			} else if err := f.b.core.Attach(mgr.ID, RemoteKey("c", top.ID)); err != nil {
				t.Fatal(err)
			}
			f.b.core.RunSelf = closeRunner(t, f.b)
			author := top
			if tc.stranger {
				author = other
			}
			_, err := f.c.core.CloseGoal(f.c.board(t), CloseOrder{Ref: RemoteKey("b", g.ID), As: CloseSuperseded, Why: "folded into goal9", Author: author.ID})
			if err != nil && !errors.Is(err, ErrFinal) {
				t.Fatal(err)
			}
			for range 4 {
				f.sync(t)
			}
			now, err := f.b.core.Store.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !tc.closes {
				if now.State != GoalActive || f.b.closure(t, g.ID) != nil {
					t.Fatalf("a stranger closed the goal: %s %+v", now.State, f.b.closure(t, g.ID))
				}
				if !slices.ContainsFunc(f.c.handedTo(other.Session), func(s string) bool { return strings.Contains(s, "your close of") && strings.Contains(s, "was refused") }) {
					t.Fatalf("stranger heard %q", f.c.handedTo(other.Session))
				}
				return
			}
			cl := f.b.closure(t, g.ID)
			if now.State != GoalDropped || cl == nil || cl.Reason != "folded into goal9" || !strings.Contains(cl.Actor, top.ID) {
				t.Fatalf("goal %s closure %+v, want dropped by %s", now.State, cl, top.ID)
			}
		})
	}
}

func TestAChiefOnAnotherHostHearsAGoalClosedBelowIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		// heardBefore has the host report something to the chief first, so the close is not the first thing it hears.
		heardBefore bool
		// retiredBetween puts a retired chief on the goal's host between its manager and the chief on the other host.
		retiredBetween bool
	}{
		{name: "after an earlier report", heardBefore: true},
		{name: "as the first thing that host reports"},
		{name: "past a retired chief between them", retiredBetween: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			top := f.c.chief(t, "org")
			g, mgr := f.b.goal(t, "ship the fix", "/repo/app")
			if tc.retiredBetween {
				mid := f.b.chief(t, "mid", mgr.ID)
				if err := f.b.core.Attach(mid.ID, RemoteKey("c", top.ID)); err != nil {
					t.Fatal(err)
				}
				f.b.supervise(t)
				if _, err := f.b.core.KillGoal(f.b.board(t), mid.ID); err != nil {
					t.Fatal(err)
				}
			} else if err := f.b.core.Attach(mgr.ID, RemoteKey("c", top.ID)); err != nil {
				t.Fatal(err)
			}
			if tc.heardBefore {
				f.b.loop(t, 1)
				f.sync(t)
			}
			if _, err := f.b.core.CloseGoal(f.b.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "owner stopped it"}); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				f.b.clock = f.b.clock.Add(31 * time.Minute)
				f.b.loop(t, 1)
				f.sync(t)
				f.sync(t)
				f.c.clock = f.c.clock.Add(31 * time.Minute)
				f.c.loop(t, 1)
			}
			heard := 0
			for _, s := range f.c.handedTo(top.Session) {
				heard += strings.Count(s, EventGoalClosed)
			}
			if heard != 1 {
				t.Fatalf("remote chief heard the close %d times: %q", heard, f.c.handedTo(top.Session))
			}
		})
	}
}

func TestAChiefAboveTheGoalHearsItsCloseOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		midRetired bool
		bossCloses bool
		wantWoken  int
	}{
		{name: "direct chief, closed by the owner", wantWoken: 1},
		{name: "above a retired chief, closed by the owner", midRetired: true, wantWoken: 1},
		{name: "the closing chief is not woken by its own close", bossCloses: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, mgr := w.goal(t, "ship", "/repo/app")
			mid := w.chief(t, "mid", mgr.ID)
			boss := w.chief(t, "boss", mid.ID)
			w.supervise(t)
			w.clock = w.clock.Add(time.Minute)
			if tc.midRetired {
				if _, err := w.core.KillGoal(w.board(t), mid.ID); err != nil {
					t.Fatal(err)
				}
			}
			author := Owner
			if tc.bossCloses {
				author = boss.ID
			}
			w.clock = w.clock.Add(time.Minute)
			if _, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "stop it", Author: author}); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				w.clock = w.clock.Add(31 * time.Minute)
				w.loop(t, 1)
			}
			woken := 0
			for _, s := range w.handedTo(boss.Session) {
				woken += strings.Count(s, EventGoalClosed)
			}
			if woken != tc.wantWoken {
				t.Fatalf("boss heard the close %d times, want %d: %q", woken, tc.wantWoken, w.handedTo(boss.Session))
			}
		})
	}
}

func TestAnIDSharedAcrossHostsGrantsNothing(t *testing.T) {
	acts := []struct {
		name string
		act  func(*orgWorld, Board, Goal, string) error
	}{
		{"close", func(w *orgWorld, b Board, g Goal, author string) error {
			_, err := w.core.CloseGoal(b, CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "not mine", Author: author})
			return err
		}},
		{"steer", func(w *orgWorld, b Board, g Goal, author string) error {
			_, err := w.core.SteerContext(b, g.ID, "new plan", author)
			return err
		}},
	}
	for _, act := range acts {
		for _, tc := range []struct {
			name string
			// reallyAbove makes the chief on the goal's host the one above the chief between, as against a namesake of the one that is.
			reallyAbove bool
		}{
			{name: "a namesake of the chief above is told no"},
			{name: "a chief truly above through another host acts", reallyAbove: true},
		} {
			t.Run(act.name+": "+tc.name, func(t *testing.T) {
				f := newFleet(t)
				top := f.a.chief(t, "top")
				mid := f.a.chief(t, "mid")
				local := f.b.chief(t, "local")
				g, mgr := f.b.goal(t, "ship", "/repo/app")
				if local.ID != top.ID {
					t.Fatalf("ids %s and %s must collide for this test", local.ID, top.ID)
				}
				parent := top.ID
				if tc.reallyAbove {
					parent = RemoteKey("b", local.ID)
				}
				if err := f.a.core.Attach(mid.ID, parent); err != nil {
					t.Fatal(err)
				}
				if err := f.b.core.Attach(mgr.ID, RemoteKey("a", mid.ID)); err != nil {
					t.Fatal(err)
				}
				if err := act.act(f.b, f.b.board(t), g, local.ID); err != nil {
					t.Fatal(err)
				}
				for range 4 {
					f.sync(t)
				}
				steers, err := f.b.core.Store.Steers(mgr.ID)
				if err != nil {
					t.Fatal(err)
				}
				now, err := f.b.core.Store.Goal(g.ID)
				if err != nil {
					t.Fatal(err)
				}
				acted := now.State == GoalDropped || len(steers) > 0
				if acted != tc.reallyAbove {
					t.Fatalf("state %s steers %+v; acted %v, want %v", now.State, steers, acted, tc.reallyAbove)
				}
				if tc.reallyAbove {
					if cl := f.b.closure(t, g.ID); cl != nil && cl.Actor != "chief "+local.ID {
						t.Fatalf("actor %q, want the chief named as this host names it", cl.Actor)
					}
					if len(steers) > 0 && steers[0].Author != local.ID {
						t.Fatalf("steer author %q, want %q", steers[0].Author, local.ID)
					}
					return
				}
				if !slices.ContainsFunc(f.b.handedTo(local.Session), func(s string) bool { return strings.Contains(s, "was refused") }) {
					t.Fatalf("the namesake heard %q, want the refusal", f.b.handedTo(local.Session))
				}
			})
		}
	}
}

func TestARelayedCloseThatCannotLandRunsOnce(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ref     string
		already bool
	}{
		{name: "unknown goal", ref: "goal999"},
		{name: "already closed", already: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			g, _ := f.b.goal(t, "ship", "/repo/app")
			if tc.already {
				if _, err := f.b.core.CloseGoal(f.b.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "first"}); err != nil {
					t.Fatal(err)
				}
			}
			ref := tc.ref
			if ref == "" {
				ref = g.ID
			}
			runs := 0
			inner := closeRunner(t, f.b)
			f.b.core.RunSelf = func(args ...string) (string, error) {
				runs++
				return inner(args...)
			}
			if _, err := f.c.core.CloseGoal(f.c.board(t), CloseOrder{Ref: RemoteKey("b", ref), As: CloseAbandoned, Why: "typo"}); err != nil {
				t.Fatal(err)
			}
			for range 5 {
				f.sync(t)
				f.b.clock = f.b.clock.Add(time.Hour)
			}
			if runs != 1 {
				t.Fatalf("the relay ran the close %d times", runs)
			}
		})
	}
}

func TestReviveAfterAutoEndStaysUp(t *testing.T) {
	for _, tc := range []struct {
		name   string
		revive func(*testing.T, *verifyWorld)
		state  string
	}{
		{"ym revive by goal, as the event says", func(t *testing.T, w *verifyWorld) {
			if _, err := w.core.ReopenGoal(w.goal.ID); err != nil {
				t.Fatal(err)
			}
		}, GoalActive},
		{"ym revive by a session's key", func(t *testing.T, w *verifyWorld) {
			w.spawn(w.manager.Session, "")
		}, GoalDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.pass(t)
			done, _ := w.core.Store.Goal(w.goal.ID)
			w.clock = done.DoneAt.Add(DoneShown + time.Minute)
			if err := w.core.archiveDone(w.board(t)); err != nil {
				t.Fatal(err)
			}
			if !w.board(t).ArchivedGoals()[0].Reopens {
				t.Fatal("the archived view does not offer to reopen an auto-ended goal")
			}
			tc.revive(t, w)
			ended := len(w.launches.forgot)
			for range 3 {
				w.clock = w.clock.Add(2 * DoneShown)
				if err := w.core.archiveDone(w.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			if len(w.launches.forgot) != ended {
				t.Fatalf("auto-end ended %v after the revive", w.launches.forgot[ended:])
			}
			n := 0
			for _, k := range w.kinds(t, w.goal.ID) {
				if k == EventGoalArchived {
					n++
				}
			}
			now, _ := w.core.Store.Goal(w.goal.ID)
			if n != 1 || now.State != tc.state {
				t.Fatalf("%d goal-archived events, state %s; want 1, %s", n, now.State, tc.state)
			}
		})
	}
}

func TestKillAfterAnArchiveLeavesTheGoalAsItIs(t *testing.T) {
	closeIt := func(t *testing.T, w *verifyWorld) {
		if _, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: w.goal.ID, As: CloseSuperseded, Why: "folded"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name  string
		end   func(*testing.T, *verifyWorld)
		state string
		holds bool
	}{
		{"closed done stays done", func(t *testing.T, w *verifyWorld) { w.pass(t); closeIt(t, w) }, GoalDone, true},
		{"closed live stays dropped", closeIt, GoalDropped, true},
		{"auto-ended done stays done", func(t *testing.T, w *verifyWorld) {
			w.pass(t)
			w.clock = w.clock.Add(DoneShown + time.Minute)
			if err := w.core.archiveDone(w.board(t)); err != nil {
				t.Fatal(err)
			}
		}, GoalDone, true},
		{"done with no close still takes the kill", func(t *testing.T, w *verifyWorld) { w.pass(t) }, GoalKilled, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			tc.end(t, w)
			was, _ := w.core.Store.Goal(w.goal.ID)
			w.clock = w.clock.Add(time.Minute)
			if _, err := w.core.KillGoal(w.board(t), w.goal.ID); err != nil {
				t.Fatal(err)
			}
			now, _ := w.core.Store.Goal(w.goal.ID)
			if now.State != tc.state {
				t.Fatalf("state %s, want %s", now.State, tc.state)
			}
			_, shown := w.board(t).Node(w.manager.ID)
			if tc.holds && (shown || !now.DoneAt.Equal(was.DoneAt) || !now.AckedAt.Equal(was.AckedAt)) {
				t.Fatalf("on tree %v, done %v was %v, acked %v was %v", shown, now.DoneAt, was.DoneAt, now.AckedAt, was.AckedAt)
			}
		})
	}
}

func TestReopeningFromTheArchive(t *testing.T) {
	for _, tc := range []struct {
		name    string
		end     func(*testing.T, *orgWorld, Goal, Manager)
		reopens bool
	}{
		{"closed", func(t *testing.T, w *orgWorld, g Goal, _ Manager) {
			if _, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: "stop"}); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"killed", func(t *testing.T, w *orgWorld, g Goal, _ Manager) {
			if _, err := w.core.KillGoal(w.board(t), g.ID); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"dropped by a move", func(t *testing.T, w *orgWorld, g Goal, _ Manager) {
			now, _ := w.core.Store.Goal(g.ID)
			now.State, now.DoneAt, now.AckedAt, now.Evidence = GoalDropped, w.clock, w.clock, "moved to box"
			if err := w.core.Store.SaveGoal(now); err != nil {
				t.Fatal(err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, m := w.goal(t, "ship", "/repo/app")
			w.spawn("agent-1", m.Session)
			it := w.ask(t, "agent-1", "which way?")
			tc.end(t, w, g, m)
			_, err := w.core.ReopenGoal(g.ID)
			now, _ := w.core.Store.Goal(g.ID)
			if !tc.reopens {
				if err == nil || now.State != GoalDropped {
					t.Fatalf("reopen: %v, state %s; want refused", err, now.State)
				}
				return
			}
			if err != nil || now.State != GoalActive || !now.DoneAt.IsZero() || !now.AckedAt.IsZero() {
				t.Fatalf("reopen: %v, goal %+v", err, now)
			}
			if w.closure(t, g.ID) != nil {
				t.Fatal("the closure still stands after a reopen")
			}
			if after, _ := w.core.Store.Item(it.Key()); tc.name == "closed" && after.State != ItemDismissed {
				t.Fatalf("reopening restored the item: %s", after.State)
			}
			o, _ := w.core.LoadOrg()
			if !o.Active(o.Managers[m.ID]) {
				t.Fatalf("ym does not supervise the reopened goal's manager: %+v", o.Managers[m.ID])
			}
			if _, err := w.core.ReopenGoal(g.ID); err == nil {
				t.Fatal("reopened a goal that is running")
			}
		})
	}
}

func TestClosingARunningGoalAfterAReopenRecordsTheNewClose(t *testing.T) {
	w := newOrgWorld(t)
	g, _ := w.goal(t, "ship", "/repo/app")
	for _, why := range []string{"first", "second"} {
		if _, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseAbandoned, Why: why}); err != nil {
			t.Fatal(err)
		}
		if cl := w.closure(t, g.ID); cl == nil || cl.Reason != why {
			t.Fatalf("closure %+v, want %q", cl, why)
		}
		if why == "first" {
			if _, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: g.ID, As: CloseSuperseded, Why: "again"}); err == nil || !errors.Is(err, ErrFinal) {
				t.Fatalf("closing a closed goal: %v", err)
			}
			if _, err := w.core.ReopenGoal(g.ID); err != nil {
				t.Fatal(err)
			}
			w.supervise(t)
		}
	}
}

func TestAutoArchiveEndsADoneGoalsLeftoversOnlyAfterADayWithoutDirection(t *testing.T) {
	brief := func(t *testing.T, w *verifyWorld) {
		if _, err := w.core.WriteBrief(w.manager.Session, Brief{Assessment: "a", Changed: "c", Uncertainty: "u", Next: "n", WaitingOn: "w"}); err != nil {
			t.Fatal(err)
		}
	}
	update := func(t *testing.T, w *verifyWorld) {
		if _, _, err := w.core.RecordGoalEvent(w.goal.ID, EventUpdate, "still landing the docs", ""); err != nil {
			t.Fatal(err)
		}
	}
	nudge := func(t *testing.T, w *verifyWorld) {
		if _, _, err := w.core.RecordGoalEvent(w.goal.ID, EventNudge, "you ended a turn", ""); err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.core.RecordGoalEvent(w.goal.ID, EventTimer, "wake", ""); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name     string
		wait     time.Duration
		activity func(*testing.T, *verifyWorld)
		at       time.Duration
		ends     bool
	}{
		{name: "just under a day stays", wait: DoneShown - time.Minute},
		{name: "a day with nothing ends", wait: DoneShown + time.Minute, ends: true},
		{name: "a day with only nudges ends", wait: DoneShown + time.Minute, activity: nudge, at: DoneShown - time.Hour, ends: true},
		{name: "a brief late in the day holds it", wait: DoneShown + time.Minute, activity: brief, at: DoneShown - time.Hour},
		{name: "an update late in the day holds it", wait: DoneShown + time.Minute, activity: update, at: DoneShown - time.Hour},
		{name: "a day after the last update ends", wait: 2*DoneShown + time.Minute, activity: update, at: time.Hour, ends: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			w.spawn("agent-1", w.manager.Session)
			w.pass(t)
			done, _ := w.core.Store.Goal(w.goal.ID)
			if tc.activity != nil {
				w.clock = done.DoneAt.Add(tc.at)
				tc.activity(t, w)
			}
			w.clock = done.DoneAt.Add(tc.wait)
			b := w.board(t)
			if err := w.core.archiveDone(b); err != nil {
				t.Fatal(err)
			}
			now, _ := w.core.Store.Goal(w.goal.ID)
			if now.State != GoalDone || !now.DoneAt.Equal(done.DoneAt) {
				t.Fatalf("goal %s done %v; a done goal stays as it is", now.State, now.DoneAt)
			}
			_, alive := w.board(t).Session(w.manager.Session)
			archived := slices.Contains(w.kinds(t, w.goal.ID), EventGoalArchived)
			if alive == tc.ends || archived != tc.ends {
				t.Fatalf("manager alive %v, archived event %v; want ended %v", alive, archived, tc.ends)
			}
		})
	}
}

func TestAutoArchiveLeavesOtherGoalsAndAChiefHearsIt(t *testing.T) {
	w := newVerifyWorld(t)
	ch := w.chief(t, "boss", w.manager.ID)
	active, activeM := w.orgWorld.goal(t, "other", "/repo/app")
	w.pass(t)
	w.tick(t, time.Second)
	w.clock = w.clock.Add(DoneShown + time.Hour)
	if err := w.core.archiveDone(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if now, _ := w.core.Store.Goal(active.ID); now.State != GoalActive {
		t.Fatalf("active goal is %s", now.State)
	}
	if _, alive := w.board(t).Session(activeM.Session); !alive {
		t.Fatal("auto-archive ended an active goal's manager")
	}
	pending, err := w.core.Store.unnotified()
	if err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(pending, func(e GoalEvent) bool { return e.Kind == EventGoalArchived }) {
		t.Fatal("auto-archive raises a desktop notification")
	}
	o, _ := w.core.LoadOrg()
	events, err := w.core.chiefEvents(o, o.Managers[ch.ID])
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(events, func(e GoalEvent) bool { return e.Kind == EventGoalArchived }) {
		t.Fatalf("chief's events %+v lack goal-archived", events)
	}
	if err := w.core.archiveDone(w.board(t)); err != nil {
		t.Fatal(err)
	}
	if n := slices.Index(w.kinds(t, w.goal.ID), EventGoalArchived); n < 0 || slices.Contains(w.kinds(t, w.goal.ID)[n+1:], EventGoalArchived) {
		t.Fatalf("archived event count wrong: %v", w.kinds(t, w.goal.ID))
	}
}

func TestVerifierOverride(t *testing.T) {
	for _, tc := range []struct {
		name     string
		as       string
		override bool
		failed   bool
		refused  string
		wants    string
	}{
		{name: "finished after a fail needs the flag", as: CloseFinished, failed: true, refused: "--override-verifier"},
		{name: "finished with no attempt needs the flag", as: CloseFinished, refused: "--override-verifier"},
		{name: "the flag records the override", as: CloseFinished, override: true, failed: true, wants: CloseFinishedOverride},
		{name: "the flag only goes with finished", as: CloseAbandoned, override: true, refused: "--override-verifier"},
		{name: "abandoned needs no verifier", as: CloseAbandoned, failed: true, wants: CloseAbandoned},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newVerifyWorld(t)
			if tc.failed {
				w.done(t, "shipped")
				w.tick(t, time.Second)
				if _, err := w.core.RecordVerdict(w.verifiers()[0], w.goal.ID, VerdictFail, []string{"ci on merge commit"}); err != nil {
					t.Fatal(err)
				}
			}
			ch := w.chief(t, "boss", w.manager.ID)
			closed, err := w.core.CloseGoal(w.board(t), CloseOrder{Ref: w.goal.ID, As: tc.as, Override: tc.override, Why: "merged, ci flake only", Author: ch.ID})
			if tc.refused != "" {
				now, _ := w.core.Store.Goal(w.goal.ID)
				if err == nil || !errors.Is(err, ErrFinal) || !strings.Contains(err.Error(), tc.refused) || now.State != GoalActive {
					t.Fatalf("err %v, state %s; want refused naming %q", err, now.State, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if closed.Disposition != tc.wants {
				t.Fatalf("disposition %q, want %q", closed.Disposition, tc.wants)
			}
			now, _ := w.core.Store.Goal(w.goal.ID)
			if now.State != GoalDropped {
				t.Fatalf("state %s; an override writes no done", now.State)
			}
			vs, err := w.core.Store.Verifications(w.goal.ID)
			if err != nil {
				t.Fatal(err)
			}
			if slices.ContainsFunc(vs, func(v Verification) bool { return v.Verdict == VerdictPass }) {
				t.Fatalf("a pass was written: %+v", vs)
			}
			if tc.failed && !slices.ContainsFunc(vs, func(v Verification) bool { return v.Verdict == VerdictFail }) {
				t.Fatalf("the fail row went: %+v", vs)
			}
			kinds := w.kinds(t, w.goal.ID)
			if slices.Contains(kinds, EventDone) || slices.Contains(kinds, EventVerified) {
				t.Fatalf("events %v report done", kinds)
			}
		})
	}
}

func TestACloseRefusesWhatIsNotAGoalOrHasNoWords(t *testing.T) {
	for _, tc := range []struct {
		name  string
		order func(goal, chief string) CloseOrder
		want  string
	}{
		{"no reason", func(g, _ string) CloseOrder { return CloseOrder{Ref: g, As: CloseAbandoned, Why: "  "} }, "--why"},
		{"unknown disposition", func(g, _ string) CloseOrder { return CloseOrder{Ref: g, As: "done", Why: "x"} }, "--as"},
		{"a chief", func(_, c string) CloseOrder { return CloseOrder{Ref: c, As: CloseAbandoned, Why: "x"} }, "is a chief"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newOrgWorld(t)
			g, _ := w.goal(t, "ship", "/repo/app")
			ch := w.chief(t, "boss")
			_, err := w.core.CloseGoal(w.board(t), tc.order(g.ID, ch.ID))
			if err == nil || !errors.Is(err, ErrFinal) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v, want final naming %q", err, tc.want)
			}
		})
	}
}
