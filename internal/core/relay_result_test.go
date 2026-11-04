package core

import (
	stderrors "errors"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/project-kikkuli/yamnaya/internal/proc"
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
				if err := f.b.core.Loop(f.b.board(t)); err != nil {
					t.Fatal(err)
				}
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
				if got := heardWith(f.b, chief.Session, RemoteKey("a", g.Name)); len(got) == 0 {
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

// breakMessages makes every message w records fail, as a store that errors does, until the returned func mends it.
func breakMessages(t *testing.T, w *orgWorld) func() {
	t.Helper()
	if _, err := w.core.Store.db.Exec(`ALTER TABLE ym_messages RENAME TO ym_messages_gone`); err != nil {
		t.Fatal(err)
	}
	return func() {
		t.Helper()
		if _, err := w.core.Store.db.Exec(`ALTER TABLE ym_messages_gone RENAME TO ym_messages`); err != nil {
			t.Fatal(err)
		}
	}
}

func recorded(t *testing.T, w *orgWorld, session, text string) int {
	t.Helper()
	var n int
	if err := w.core.Store.db.QueryRow(`SELECT count(*) FROM ym_messages WHERE session = ? AND text LIKE ?`, session, "%"+text+"%").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAnOutcomeIsToldOnceHoweverTheHostReportingItFails(t *testing.T) {
	failing := map[string]func(t *testing.T, w *orgWorld) func(){
		"its board cannot be read": func(t *testing.T, w *orgWorld) func() {
			procs := w.core.Procs
			w.core.Procs = func() ([]proc.Proc, error) { return nil, stderrors.New("ps failed") }
			return func() { w.core.Procs = procs }
		},
		"its store cannot record a message": breakMessages,
	}
	for how, fail := range failing {
		for _, tc := range []struct {
			name string
			// dest runs b's command; reporter is the host whose failure is under test. With its own command in flight a's own report
			// fails while it carries b's outcome back.
			dest, reporter string
			final, ownToo  bool
		}{
			{name: "the origin, on the confirming call", dest: "a", reporter: "b"},
			{name: "the origin, refused for good", dest: "a", reporter: "b", final: true},
			{name: "the middle host, whose own report fails too", dest: "c", reporter: "a", ownToo: true},
		} {
			t.Run(how+"/"+tc.name, func(t *testing.T) {
				f := newFleet(t)
				chief := f.b.chief(t, "platform")
				local := f.a.chief(t, "local")
				run := func(args ...string) (string, error) {
					if tc.final {
						return "", Final(stderrors.New("the marker"))
					}
					return "the marker " + args[2], nil
				}
				map[string]*orgWorld{"a": f.a, "c": f.c}[tc.dest].core.RunSelf = run
				if tc.ownToo {
					if err := f.a.core.Relay("c", RelayYM, relayYM{Args: []string{"item", "answer", "#1", "yes"}, Requester: local.Session}); err != nil {
						t.Fatal(err)
					}
				}
				f.b.core.Requester = func() string { return chief.Session }
				if _, err := f.b.core.RunOrRelay(tc.dest, "item", "answer", "#3", "yes"); err != nil {
					t.Fatal(err)
				}
				mend := fail(t, map[string]*orgWorld{"a": f.a, "b": f.b}[tc.reporter])
				cycleAll(f, 4)
				mend()
				cycleAll(f, 4)
				if tc.final {
					if asked := questions(t, f.b); len(asked) != 1 {
						t.Errorf("requester's items %q, want the refusal once", asked)
					}
				} else if n := recorded(t, f.b, chief.Session, "the marker #3"); n != 1 {
					t.Errorf("requester was told %d times, want once", n)
				}
				if tc.ownToo {
					if n := recorded(t, f.a, local.Session, "the marker #1"); n != 1 {
						t.Errorf("a's own requester was told %d times, want once", n)
					}
				}
				for name, w := range map[string]*orgWorld{"a": f.a, "b": f.b} {
					if pending, _ := w.core.Store.PendingRelays(); len(pending) != 0 {
						t.Errorf("%s still holds %+v", name, pending)
					}
				}
			})
		}
	}
}

func TestAHostThatCannotReportStillCarriesRelaysToTheNextHost(t *testing.T) {
	f := newFleet(t)
	local := f.a.chief(t, "local")
	f.b.core.RunSelf = func(args ...string) (string, error) { return "ran", nil }
	var ran []string
	f.c.core.RunSelf = func(args ...string) (string, error) {
		ran = append(ran, strings.Join(args, " "))
		return "ran", nil
	}
	for _, dest := range []string{"b", "c"} {
		if err := f.a.core.Relay(dest, RelayYM, relayYM{Args: []string{"item", "answer", "#1", dest}, Requester: local.Session}); err != nil {
			t.Fatal(err)
		}
	}
	defer breakMessages(t, f.a)()
	cycleAll(f, 1)
	if !slices.Contains(ran, "item answer #1 c") {
		t.Errorf("c ran %q while a could not report b's outcome, want its command carried", ran)
	}
}

func TestARequesterHearsDirectlyWhenTheGoalsFeedWillNotReachIt(t *testing.T) {
	for _, tc := range []struct {
		name string
		end  func(t *testing.T, f *fleet, g Goal, m, chief Manager)
		// feed says the goal's feed still reaches the chief, so it hears there and not directly.
		feed  bool
		final bool
	}{
		{name: "a live goal", feed: true},
		{name: "a goal that is done", end: func(t *testing.T, f *fleet, g Goal, _, _ Manager) {
			got, err := f.a.core.Store.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			got.State = GoalDone
			if err := f.a.core.Store.SaveGoal(got); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a goal that is done, refused for good", final: true, end: func(t *testing.T, f *fleet, g Goal, _, _ Manager) {
			got, err := f.a.core.Store.Goal(g.ID)
			if err != nil {
				t.Fatal(err)
			}
			got.State = GoalDone
			if err := f.a.core.Store.SaveGoal(got); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a retired manager", end: func(t *testing.T, f *fleet, _ Goal, m, _ Manager) {
			m.Retired = true
			if err := f.a.core.Store.SaveManager(m); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "a retired chief asking", end: func(t *testing.T, f *fleet, _ Goal, _, chief Manager) {
			chief.Retired = true
			if err := f.b.core.Store.SaveManager(chief); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "platform")
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
				if err := f.b.core.Loop(f.b.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			cycle()
			if tc.end != nil {
				m, _ = f.a.core.Store.Manager(m.ID)
				tc.end(t, f, g, m, chief)
			}
			cycle()
			f.a.core.RunSelf = func(args ...string) (string, error) {
				if tc.final {
					return "", Final(stderrors.New("the marker"))
				}
				return "the marker", nil
			}
			f.b.core.Requester = func() string { return chief.Session }
			if err := f.b.core.AckGoal(RemoteKey("a", g.ID)); err != nil {
				t.Fatal(err)
			}
			for range 5 {
				cycle()
			}
			direct, asked := recorded(t, f.b, chief.Session, "ym: ym goal ack"), questions(t, f.b)
			switch {
			case tc.final:
				if len(asked) != 1 || direct != 0 {
					t.Errorf("requester was told %d times and asked %q, want the refusal in one item", direct, asked)
				}
			case tc.feed:
				if direct != 0 || recorded(t, f.b, chief.Session, "the marker") != 1 {
					t.Errorf("chief heard directly %d times, feed %d; want the feed once", direct, recorded(t, f.b, chief.Session, "the marker"))
				}
			default:
				if direct != 1 {
					t.Errorf("requester was told %d times, want once directly: %q", direct, f.b.handedTo(chief.Session))
				}
			}
		})
	}
}
