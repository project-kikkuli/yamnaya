package core

import (
	stderrors "errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func cycleAll(f *fleet, n int) {
	for range n {
		f.a.core.SyncRelays([]string{"b", "c"}, f.net)
	}
}

func heardWith(w *orgWorld, session, text string) []string {
	var out []string
	for _, h := range w.handedTo(session) {
		if strings.Contains(h, text) {
			out = append(out, h)
		}
	}
	return out
}

func questions(t *testing.T, w *orgWorld) []string {
	t.Helper()
	items, err := w.core.Store.Items(ItemFilter{})
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, it := range items {
		out = append(out, it.Question)
	}
	return out
}

func TestAnOutcomeSurvivesALinkLostBeforeItWasConfirmed(t *testing.T) {
	for _, tc := range []struct {
		name string
		// dest pulls b's relays and loses its confirming call, or is reached by the puller, whose call loses its answer.
		dest                string
		final               bool
		loseAcks, loseReply int
	}{
		{name: "the puller ran it and its confirmation was lost", dest: "a", loseAcks: 1},
		{name: "the puller refused it for good and its confirmation was lost", dest: "a", final: true, loseAcks: 1},
		{name: "a host past the puller ran it and the answer was lost", dest: "c", loseReply: 1},
		{name: "a host past the puller refused it for good and the answer was lost", dest: "c", final: true, loseReply: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "platform")
			runs := 0
			map[string]*orgWorld{"a": f.a, "c": f.c}[tc.dest].core.RunSelf = func(args ...string) (string, error) {
				runs++
				if tc.final {
					return "", Final(stderrors.New("no such item"))
				}
				return "answered #3", nil
			}
			f.b.core.Requester = func() string { return chief.Session }
			if _, err := f.b.core.RunOrRelay(tc.dest, "item", "answer", "#3", "yes"); err != nil {
				t.Fatal(err)
			}
			f.net.loseAcks, f.net.loseReplies = tc.loseAcks, tc.loseReply
			cycleAll(f, 5)
			if runs != 1 {
				t.Errorf("%s ran the command %d times, want once", tc.dest, runs)
			}
			told, asked := heardWith(f.b, chief.Session, "answered #3"), questions(t, f.b)
			if tc.final {
				if len(asked) != 1 || !strings.Contains(asked[0], "no such item") {
					t.Errorf("requester's items %q, want the refusal once", asked)
				}
				return
			}
			if len(told) != 1 || len(asked) != 0 {
				t.Errorf("requester was told %q and asked %q, want the output once", told, asked)
			}
		})
	}
}

func TestAnOutcomeLandsOnTheGoalACommandTargetsAndNoOther(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(m Manager, g Goal) []string
		// recorded says the command targets the existing goal, so its feed records the outcome.
		recorded bool
	}{
		{"a relaunch", func(m Manager, _ Goal) []string { return []string{"goal", "relaunch", m.ID} }, true},
		{"an ack by goal", func(_ Manager, g Goal) []string { return []string{"goal", "ack", g.ID} }, true},
		{"a send past its flags", func(m Manager, _ Goal) []string { return []string{"goal", "send", "--relayed", m.ID, "hello"} }, true},
		{"a steer whose text names another", func(m Manager, _ Goal) []string { return []string{"goal", "steer", "--text", "mgr99", m.ID} }, true},
		{"a chief attach", func(m Manager, _ Goal) []string { return []string{"chief", "attach", m.ID, "b/mgr1"} }, true},
		{"a goal start whose ask is the goal's name", func(m Manager, _ Goal) []string { return []string{"goal", "--repo", "app", "--", m.Name} }, false},
		{"a goal start whose ask is the manager's id", func(m Manager, _ Goal) []string { return []string{"goal", "--repo", "app", "--", m.ID} }, false},
		{"a goal start whose repo is the manager's id", func(m Manager, _ Goal) []string { return []string{"goal", "--repo", m.ID, "--", "other"} }, false},
		{"a goal start under the manager", func(m Manager, _ Goal) []string {
			return []string{"goal", "--repo", "app", "--under", m.ID, "--", "other"}
		}, false},
		{"an item answered with the manager's id", func(m Manager, _ Goal) []string { return []string{"item", "answer", "#3", m.ID} }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "platform")
			g, m := f.a.goal(t, "ship the fix", "/repo/app")
			f.a.core.RunSelf = func(args ...string) (string, error) { return "ran it", nil }
			f.b.core.Requester = func() string { return chief.Session }
			if _, err := f.b.core.RunOrRelay("a", tc.args(m, g)...); err != nil {
				t.Fatal(err)
			}
			cycleAll(f, 3)
			events, err := f.a.core.Store.GoalEvents(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got := slices.ContainsFunc(events, func(e GoalEvent) bool { return e.Kind == EventRelayed }); got != tc.recorded {
				t.Errorf("goal's feed recorded the outcome = %v, want %v: %+v", got, tc.recorded, events)
			}
			if told := heardWith(f.b, chief.Session, "ran it"); len(told) != 1 {
				t.Errorf("requester was told %q, want the output once", told)
			}
		})
	}
}

func TestTheRequesterHearsAGoalCommandsOutcomeOnceWhoeverItIs(t *testing.T) {
	for _, tc := range []struct {
		name string
		// rung is the requester: the goal's chief, the chief above that one, or a chief the goal does not report to.
		rung  string
		final bool
	}{
		{name: "the goal's chief hears it ran", rung: "chief"},
		{name: "the chief above hears it ran", rung: "above"},
		{name: "an unrelated chief hears it ran", rung: "unrelated"},
		{name: "the goal's chief hears it was refused", rung: "chief", final: true},
		{name: "an unrelated chief hears it was refused", rung: "unrelated", final: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			above := f.b.chief(t, "org")
			chief := f.b.chief(t, "platform")
			if err := f.b.core.Attach(chief.ID, above.ID); err != nil {
				t.Fatal(err)
			}
			other := f.b.chief(t, "elsewhere")
			g, m := f.a.goal(t, "ship the fix", "/repo/app")
			if err := f.a.core.Attach(m.ID, RemoteKey("b", chief.ID)); err != nil {
				t.Fatal(err)
			}
			cycle := func() {
				t.Helper()
				if err := f.a.core.Loop(f.a.board(t)); err != nil {
					t.Fatal(err)
				}
				cycleAll(f, 1)
			}
			cycle()
			f.a.core.RunSelf = func(args ...string) (string, error) {
				if tc.final {
					return "", Final(stderrors.New("the marker"))
				}
				return "the marker", nil
			}
			requester := map[string]Manager{"chief": chief, "above": above, "unrelated": other}[tc.rung]
			f.b.core.Requester = func() string { return requester.Session }
			if _, err := f.b.core.ReplaceManager(Board{}, RemoteKey("a", m.ID)); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				cycle()
			}
			heard := len(heardWith(f.b, requester.Session, "the marker"))
			if heard != 1 {
				t.Errorf("requester %s was handed the outcome %d times, want once: %q", tc.rung, heard, f.b.handedTo(requester.Session))
			}
			if tc.rung == "unrelated" {
				if got := heardWith(f.b, chief.Session, RemoteKey("a", g.ID)); len(got) == 0 {
					t.Errorf("the goal's chief heard nothing of it: %q", f.b.handedTo(chief.Session))
				}
			}
		})
	}
}

func TestARefusalThatMayClearFilesNoItemAndItsDropAdviceWorksWhereItSaysTo(t *testing.T) {
	for _, tc := range []struct {
		name, dest string
		// holder is the host the advice must name and whose row it must end; b's own row needs no host named.
		holder string
	}{
		{name: "refused by the host that pulls, so b holds the row", dest: "a", holder: "b"},
		{name: "refused past the puller, which holds the row", dest: "c", holder: "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "platform")
			map[string]*orgWorld{"a": f.a, "c": f.c}[tc.dest].core.RunSelf = func(args ...string) (string, error) {
				return "", stderrors.New("database is locked")
			}
			f.b.core.Requester = func() string { return chief.Session }
			if _, err := f.b.core.RunOrRelay(tc.dest, "item", "answer", "#3", "yes"); err != nil {
				t.Fatal(err)
			}
			cycleAll(f, 5)
			if asked := questions(t, f.b); len(asked) != 0 {
				t.Errorf("a refusal that may clear filed %q", asked)
			}
			told := heardWith(f.b, chief.Session, "database is locked")
			if len(told) != 1 {
				t.Fatalf("requester was told %q, want the refusal once", told)
			}
			m := regexp.MustCompile(`ym relay drop (\d+)( on (\S+))?`).FindStringSubmatch(told[0])
			if m == nil {
				t.Fatalf("no drop advice in %q", told[0])
			}
			holder := "b"
			if m[3] != "" {
				holder = m[3]
			}
			if holder != tc.holder {
				t.Fatalf("advice %q is for %s, want %s", told[0], holder, tc.holder)
			}
			seq, _ := strconv.ParseInt(m[1], 10, 64)
			ok, err := map[string]*orgWorld{"a": f.a, "b": f.b}[holder].core.Store.DropRelay(seq)
			if err != nil || !ok {
				t.Errorf("following the advice on %s: dropped %v, %v", holder, ok, err)
			}
			if pending, _ := f.a.core.Store.PendingRelays(); holder == "a" && len(pending) != 0 {
				t.Errorf("a still carries %+v", pending)
			}
		})
	}
}
