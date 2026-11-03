package core

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// hostNet carries relay round trips between in-process hosts, and drops every one to a host that is down.
type hostNet struct {
	hosts map[string]*orgWorld
	down  map[string]bool
	// loseAcks drops that many round trips that confirm relays, as a link that fails mid-cycle does.
	loseAcks int
}

func (n *hostNet) Sync(host string, req RelaySync) (RelayReply, error) {
	if n.down[host] {
		return RelayReply{}, fmt.Errorf("ssh %s: %w", host, ErrUnreachable)
	}
	if len(req.Ack) > 0 && n.loseAcks > 0 {
		n.loseAcks--
		return RelayReply{}, fmt.Errorf("ssh %s: %w", host, ErrUnreachable)
	}
	reply, err := n.hosts[host].core.ServeSync(req)
	// ym relay sync prints a failed act on stderr and still answers, as here.
	if reply.Entries != nil {
		return reply, nil
	}
	return reply, err
}

// fleet is an owner's host "a" that reaches hosts "b" and "c" over the net.
type fleet struct {
	net     *hostNet
	a, b, c *orgWorld
}

func newFleet(t *testing.T) *fleet {
	t.Helper()
	f := &fleet{net: &hostNet{hosts: map[string]*orgWorld{}, down: map[string]bool{}}}
	f.a, f.b, f.c = newOrgWorld(t), newOrgWorld(t), newOrgWorld(t)
	if err := f.a.core.Store.setRelaySelf(RelaySelf{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	f.net.hosts["b"], f.net.hosts["c"] = f.b, f.c
	f.sync(t)
	return f
}

// sync runs one relay cycle on the owner's host.
func (f *fleet) sync(t *testing.T) {
	t.Helper()
	if err := f.a.core.SyncRelays([]string{"b", "c"}, f.net); err != nil && !errors.Is(err, ErrUnreachable) {
		t.Fatal(err)
	}
}

func lastHanded(w *orgWorld, session string) string {
	got := w.handedTo(session)
	if len(got) == 0 {
		return ""
	}
	return got[len(got)-1]
}

func TestAnAskClimbsToAChiefOnAnotherHostAndComesBackWhenThatChiefCannotAnswer(t *testing.T) {
	for _, tc := range []struct {
		name string
		// chiefOn is where the chief runs and managerOn where the goal's manager runs.
		chiefOn, managerOn string
		// chiefGone retires the chief before the ask, so it climbs past it to the owner.
		chiefGone bool
		// unnamed asks before the owner's host has ever named the manager's host, which then knows itself only by its host name.
		unnamed bool
	}{
		{name: "manager on b reports to a chief on a", chiefOn: "a", managerOn: "b"},
		{name: "manager on a reports to a chief on b", chiefOn: "b", managerOn: "a"},
		{name: "manager on b reports to a chief on c, two hops through a", chiefOn: "c", managerOn: "b"},
		{name: "chief on a is retired, so the ask reaches the owner on b", chiefOn: "a", managerOn: "b", chiefGone: true},
		{name: "manager on b asks before a named b", chiefOn: "a", managerOn: "b", unnamed: true},
		{name: "manager on b asks before a named b and the chief on a is retired", chiefOn: "a", managerOn: "b", unnamed: true, chiefGone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			on := map[string]*orgWorld{"a": f.a, "b": f.b, "c": f.c}
			cw, mw := on[tc.chiefOn], on[tc.managerOn]
			chief := cw.chief(t, "platform")
			_, mgr := mw.goal(t, "ship the fix", "/repo/app")
			parent := RemoteKey(tc.chiefOn, chief.ID)
			if err := mw.core.Attach(mgr.ID, parent); err != nil {
				t.Fatal(err)
			}
			if tc.chiefGone {
				if _, err := cw.core.KillGoal(cw.board(t), chief.ID); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unnamed {
				if _, err := mw.core.Store.db.Exec(`DELETE FROM ym_relay_self`); err != nil {
					t.Fatal(err)
				}
			}
			agent := "agent-" + tc.managerOn
			mw.spawn(agent, mgr.Session)
			it := mw.ask(t, agent, "which flag?")
			if it.Recipient != mgr.ID {
				t.Fatalf("an agent's ask goes to its manager first, got %q", it.Recipient)
			}
			esc := mw.ask(t, mgr.Session, "may I drop the old API?")
			if esc.Recipient != parent {
				t.Fatalf("the manager's escalation is routed to %q, want the chief on the other host %q", esc.Recipient, parent)
			}
			for range 3 {
				f.sync(t)
			}
			got := lastHanded(cw, chief.Session)
			key := RemoteKey(tc.managerOn, esc.Key())
			if tc.chiefGone {
				if strings.Contains(got, "drop the old API") {
					t.Errorf("a retired chief was handed the ask: %q", got)
				}
				back, err := mw.core.Store.Item(esc.Key())
				if err != nil {
					t.Fatal(err)
				}
				if back.Recipient != Owner {
					t.Errorf("an ask no chief can answer goes to the owner on its own host, recipient %q", back.Recipient)
				}
				return
			}
			if !strings.Contains(got, "drop the old API") || !strings.Contains(got, key+" ask from") {
				t.Errorf("chief on %s was handed %q; want the ask with its key %s as that host names it", tc.chiefOn, got, key)
			}
		})
	}
}

func TestRelaysWaitOutAHostThatIsDownAndTakeEffectOnceWhenItIsBack(t *testing.T) {
	f := newFleet(t)
	chief := f.a.chief(t, "platform")
	_, mgr := f.b.goal(t, "ship the fix", "/repo/app")
	if err := f.b.core.Attach(mgr.ID, RemoteKey("a", chief.ID)); err != nil {
		t.Fatal(err)
	}
	f.net.down["b"] = true
	esc := f.b.ask(t, mgr.Session, "may I drop the old API?")
	for range 3 {
		f.sync(t)
	}
	if got := f.a.handedTo(chief.Session); len(got) != 0 {
		t.Fatalf("chief heard %q while b was down", got)
	}
	pending, err := f.b.core.Store.PendingRelays()
	if err != nil || len(pending) != 1 {
		t.Fatalf("b holds %d relays (%v), want the ask kept while a cannot reach it", len(pending), err)
	}
	f.net.down["b"], f.net.loseAcks = false, 1
	for range 3 {
		f.sync(t)
	}
	want := RemoteKey("b", esc.Key())
	var heard int
	for _, h := range f.a.handedTo(chief.Session) {
		if strings.Contains(h, want) {
			heard++
		}
	}
	if heard != 1 {
		t.Errorf("chief heard the ask %d times after b came back, want once: %q", heard, f.a.handedTo(chief.Session))
	}
	if pending, _ := f.b.core.Store.PendingRelays(); len(pending) != 0 {
		t.Errorf("b still holds %d relays after a carried them", len(pending))
	}
}

func TestARelayedCommandRunsOnTheHostItNames(t *testing.T) {
	f := newFleet(t)
	var ran [][]string
	f.b.core.RunSelf = func(args ...string) (string, error) {
		ran = append(ran, args)
		return "", nil
	}
	if self, _ := f.b.core.Store.RelaySelf(); self.Courier != "a" || self.Name != "b" {
		t.Fatalf("b learned %+v from a's sync, want to be named b by courier a", self)
	}
	out, err := f.b.core.RunOrRelay("c", "item", "answer", "#3", "yes")
	if err != nil || !strings.Contains(out, "queued for c") {
		t.Fatalf("RunOrRelay = %q, %v", out, err)
	}
	f.c.core.RunSelf = func(args ...string) (string, error) {
		ran = append(ran, append([]string{"c"}, args...))
		return "", nil
	}
	for range 3 {
		f.sync(t)
	}
	if len(ran) != 1 || strings.Join(ran[0], " ") != "c item answer #3 yes" {
		t.Errorf("ran %q, want b's answer run once on c", ran)
	}
}

func TestAnotherHostsGoalsJoinTheBoardsTreeAndTheOwnerSteersThemThere(t *testing.T) {
	w := newOrgWorld(t)
	if err := w.core.Store.setRelaySelf(RelaySelf{Name: "a"}); err != nil {
		t.Fatal(err)
	}
	chief := w.chief(t, "platform")
	r := &fakeRemotes{boards: []RemoteBoard{{Host: "box", At: w.clock, Self: "box",
		Sessions: []Live{{Session: Session{Harness: "claude", ID: "box-mgr-session", Status: Busy}, Project: "box-mgr-session", Remote: "box"}},
		Managers: []Manager{
			{ID: "mgr1", Role: RoleManager, Name: "ship it", GoalID: "goal1", ParentID: RemoteKey("a", chief.ID), Session: "box-mgr-session"},
			{ID: "mgr2", Role: RoleManager, Name: "port it", GoalID: "goal2"},
		},
		Goals: []Goal{
			{ID: "goal1", Ask: "ship it", ManagerID: "mgr1", State: GoalActive, LastMovedAt: w.clock},
			{ID: "goal2", Ask: "port it", ManagerID: "mgr2", State: GoalStuck, Evidence: "nothing moved"},
		},
		ManagerSessions: map[string]string{"box-mgr-session": "mgr1"},
		Standing:        map[string]string{"mgr1": StandMoving, "mgr2": StandStuck},
	}}}
	w.core.Remotes, w.core.ShowRemotes = r, true
	b := w.board(t)
	top, ok := b.Node(chief.ID)
	if !ok || len(top.Children) != 1 || top.Children[0].Manager.ID != "box/mgr1" || top.Children[0].Manager.Host != "box" {
		t.Fatalf("chief %s's reports = %+v, want box's manager under it", chief.ID, top.Children)
	}
	if top.Children[0].Live == nil || top.Children[0].Standing != StandMoving {
		t.Errorf("box/mgr1 live %v standing %q, want its session and box's own reading", top.Children[0].Live, top.Children[0].Standing)
	}
	if n, ok := b.Node("box/mgr2"); !ok || n.Standing != StandStuck {
		t.Errorf("box's unattached goal = %+v, want a root stuck by box's reading", n)
	}
	var queued []string
	for _, n := range b.Needs {
		if n.Goal != nil {
			queued = append(queued, n.Goal.ID)
		}
	}
	if !slices.Contains(queued, "box/goal2") {
		t.Errorf("owner's queue has goals %v, want box's stuck goal", queued)
	}
	for _, act := range []struct {
		do   func() error
		want string
	}{
		{func() error { return w.core.AckGoal("box/goal2") }, "box goal ack goal2"},
		{func() error { _, err := w.core.KillGoal(b, "box/goal2"); return err }, "box kill --yes goal2"},
		{func() error { _, err := w.core.ReplaceManager(b, "box/mgr1"); return err }, "box goal relaunch mgr1"},
		{func() error { _, err := w.core.Steer(b, []string{chief.ID}, true, true, "focus"); return err }, "box goal send --subtree mgr1 focus"},
		{func() error { return w.core.Attach("box/mgr2", chief.ID) }, "box chief attach mgr2 a/" + chief.ID},
		{func() error { return w.core.Attach("box/mgr1", "") }, "box chief detach mgr1"},
		{func() error { _, err := w.core.SteerContext(b, "box/mgr1", "ship it smaller", Owner); return err }, "box goal steer --text ship it smaller mgr1"},
		{func() error { _, err := w.core.SteerContext(b, "box/mgr1", "ship it smaller", chief.ID); return err },
			"box goal steer --text ship it smaller --by a/" + chief.ID + " mgr1"},
	} {
		r.ran = nil
		if err := act.do(); err != nil {
			t.Fatalf("%s: %v", act.want, err)
		}
		if len(r.ran) != 1 || strings.Join(r.ran[0], " ") != act.want {
			t.Errorf("ran %q, want %q", r.ran, act.want)
		}
	}
}

func TestAChiefOnAnotherHostHearsItsGoalsAndTheOwnerIsNotifiedWhereTheySit(t *testing.T) {
	f := newFleet(t)
	var notified []string
	f.a.core.Notify = func(title, body string) error {
		notified = append(notified, title+": "+body)
		return nil
	}
	top := f.a.chief(t, "org")
	chief := f.a.chief(t, "platform")
	if err := f.a.core.Attach(chief.ID, top.ID); err != nil {
		t.Fatal(err)
	}
	g, mgr := f.b.goal(t, "ship the fix", "/repo/app")
	if err := f.b.core.Attach(mgr.ID, RemoteKey("a", chief.ID)); err != nil {
		t.Fatal(err)
	}
	cycle := func() {
		t.Helper()
		if err := f.b.core.Loop(f.b.board(t)); err != nil {
			t.Fatal(err)
		}
		f.sync(t)
		f.sync(t)
		if err := f.a.core.Loop(f.a.board(t)); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := f.b.core.RecordGoalEvent(g.ID, EventUpdate, "before the chief", ""); err != nil {
		t.Fatal(err)
	}
	cycle()
	if _, _, err := f.b.core.RecordGoalEvent(g.ID, EventMerged, "PR 7 merged", ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.b.core.RecordGoalEvent(g.ID, EventDone, "shipped", ""); err != nil {
		t.Fatal(err)
	}
	cycle()
	cycle()
	for _, who := range []Manager{chief, top} {
		var heard []string
		for _, h := range f.a.handedTo(who.Session) {
			if strings.Contains(h, "b/"+g.Name) {
				heard = append(heard, h)
			}
		}
		if len(heard) != 1 || !strings.Contains(heard[0], "done #") || !strings.Contains(heard[0], "1 merged PR") || strings.Contains(heard[0], "update") {
			t.Errorf("%s heard %q; want one line for b's goal, done with its merge counted, and nothing from before it was told", who.Name, heard)
		}
	}
	if len(notified) != 1 || !strings.HasPrefix(notified[0], "ym: b "+g.Name+" done") {
		t.Errorf("owner's host notified %q, want b's done goal once", notified)
	}
}

func TestAttachingUnderAChiefOnAnotherHostNamesThatChiefHoweverItIsReferred(t *testing.T) {
	for _, tc := range []struct {
		ref, parent, refused string
	}{
		{ref: "box/mgr9", parent: "box/mgr9"},
		{ref: "BOX/MGR9", parent: "box/mgr9"},
		{ref: "box/mgr3", refused: "only a chief has reports"},
		{ref: "box/goal3", refused: "only a chief has reports"},
		{ref: "box/mgr7", parent: "box/mgr7"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			w := newOrgWorld(t)
			_, mgr := w.goal(t, "ship it", "/repo/app")
			w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "box", At: w.clock, Self: "box",
				Managers: []Manager{
					{ID: "mgr9", Role: RoleChief, Name: "platform"},
					{ID: "mgr3", Role: RoleManager, Name: "port it", GoalID: "goal3"},
				},
				Goals: []Goal{{ID: "goal3", Ask: "port it", ManagerID: "mgr3", State: GoalActive}},
			}}}
			err := w.core.Attach(mgr.ID, tc.ref)
			if tc.refused != "" {
				if err == nil || !strings.Contains(err.Error(), tc.refused) {
					t.Fatalf("attach under %s = %v, want refused: %s", tc.ref, err, tc.refused)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			o, err := w.core.LoadOrg()
			if err != nil {
				t.Fatal(err)
			}
			if got := o.Managers[mgr.ID].ParentID; got != tc.parent {
				t.Errorf("attach under %s left parent %q, want %q", tc.ref, got, tc.parent)
			}
		})
	}
}

func TestARelayWhoseActFailsStaysUnackedAndTakesEffectOnceItSucceeds(t *testing.T) {
	for _, tc := range []struct {
		name string
		// from is the host that queues the command; it runs on c, so b's runs through a's carrying and c's through a directly.
		from string
	}{{name: "carried by the courier on to a third host", from: "b"}, {name: "handed straight to the host it names", from: "a"}} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			fails := 1 << 10
			var ran []string
			f.c.core.RunSelf = func(args ...string) (string, error) {
				if fails > 0 {
					fails--
					return "", errors.New("ym item answer #3: database is locked")
				}
				ran = append(ran, strings.Join(args, " "))
				return "", nil
			}
			from := map[string]*orgWorld{"a": f.a, "b": f.b}[tc.from]
			if tc.from == "a" {
				if err := from.core.Relay("c", RelayYM, relayYM{Args: []string{"item", "answer", "#3", "yes"}}); err != nil {
					t.Fatal(err)
				}
			} else if _, err := from.core.RunOrRelay("c", "item", "answer", "#3", "yes"); err != nil {
				t.Fatal(err)
			}
			for i := range 4 {
				f.a.core.SyncRelays([]string{"b", "c"}, f.net)
				if i == 1 {
					held, _ := f.a.core.Store.PendingRelays()
					if len(held) != 1 || !strings.Contains(held[0].Error, "database is locked") {
						t.Errorf("a holds %+v after c refused the command, want it kept with c's reason", held)
					}
					fails = 0
				}
			}
			if len(ran) != 1 || ran[0] != "item answer #3 yes" {
				t.Errorf("ran %q after one failed run, want the command run once it can be", ran)
			}
			for name, w := range map[string]*orgWorld{"a": f.a, "b": f.b} {
				if pending, _ := w.core.Store.PendingRelays(); len(pending) != 0 {
					t.Errorf("%s still holds %d relays after c took the command", name, len(pending))
				}
			}
		})
	}
}

func TestARelayRefusedForGoodEndsAndLeavesItsOutcomeWhereTheOwnerLooks(t *testing.T) {
	for _, tc := range []struct {
		name string
		// before is what happens on c before the owner's answer arrives; want is in the item's history afterwards.
		before func(t *testing.T, c *orgWorld, it Item)
		ref    func(it Item) string
		want   string
	}{
		{name: "answered there first", before: func(t *testing.T, c *orgWorld, it Item) {
			if _, err := c.core.Store.Transition(it.Key(), ItemAnswered, "D"); err != nil {
				t.Fatal(err)
			}
		}, ref: func(it Item) string { return it.Key() }, want: "answered there already: D"},
		{name: "dismissed there first", before: func(t *testing.T, c *orgWorld, it Item) {
			if _, err := c.core.Store.Transition(it.Key(), ItemDismissed, "moot"); err != nil {
				t.Fatal(err)
			}
		}, ref: func(it Item) string { return it.Key() }, want: "dismissed there already: moot"},
		{name: "no such item there", before: func(*testing.T, *orgWorld, Item) {}, ref: func(Item) string { return "#99" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			_, mgr := f.c.goal(t, "ship the fix", "/repo/app")
			it := f.c.ask(t, mgr.Session, "C or D?")
			tc.before(t, f.c, it)
			runs := 0
			f.c.core.RunSelf = func(args ...string) (string, error) {
				runs++
				_, err := f.c.core.Store.Transition(args[2], ItemAnswered, args[3])
				return "", err
			}
			if err := f.a.core.Relay("c", RelayYM, relayYM{Args: []string{"item", "answer", tc.ref(it), "C"}}); err != nil {
				t.Fatal(err)
			}
			for range 4 {
				f.sync(t)
			}
			if runs != 1 {
				t.Errorf("c ran the answer %d times, want once: a final refusal is not tried again", runs)
			}
			if pending, _ := f.a.core.Store.PendingRelays(); len(pending) != 0 {
				t.Errorf("a still holds %+v after c refused it for good", pending)
			}
			if b := f.a.board(t); slices.ContainsFunc(b.Needs, func(n Need) bool { return n.Relay != nil }) {
				t.Errorf("owner queue shows a relay that ended")
			}
			if tc.want == "" {
				return
			}
			got, err := f.c.core.Store.Item(it.Key())
			if err != nil {
				t.Fatal(err)
			}
			if !slices.ContainsFunc(got.History, func(e Event) bool {
				return strings.Contains(e.Text, "item answer "+it.Key()+" C") && strings.Contains(e.Text, tc.want)
			}) {
				t.Errorf("item history %+v, want the owner's answer that did not land and %q", got.History, tc.want)
			}
		})
	}
}

func TestARelayRefusedOverAndOverReachesTheOwnersQueueUntilDropped(t *testing.T) {
	for _, tc := range []struct {
		name string
		// syncs is how many cycles c refuses it, wait how long passes after the first.
		syncs int
		wait  time.Duration
		shown bool
	}{
		{name: "refused twice", syncs: RefusalsBeforeOwner - 1},
		{name: "refused enough times", syncs: RefusalsBeforeOwner, shown: true},
		{name: "refused once long ago", syncs: 1, wait: RefusedBeforeOwner, shown: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			f.c.core.RunSelf = func(args ...string) (string, error) { return "", errors.New("database is locked") }
			if err := f.a.core.Relay("c", RelayYM, relayYM{Args: []string{"item", "answer", "#3", "yes"}}); err != nil {
				t.Fatal(err)
			}
			for range tc.syncs {
				f.sync(t)
			}
			f.a.clock = f.a.clock.Add(tc.wait)
			b := f.a.board(t)
			i := slices.IndexFunc(b.Needs, func(n Need) bool { return n.Relay != nil })
			if (i >= 0) != tc.shown {
				t.Fatalf("owner queue shows the refused relay = %v, want %v", i >= 0, tc.shown)
			}
			if i < 0 {
				return
			}
			n := b.Needs[i]
			if !strings.Contains(n.Relay.Error, "database is locked") || n.Key() != "relay:"+strconv.FormatInt(n.Relay.Seq, 10) {
				t.Errorf("row %+v key %s, want c's reason and the relay's own seq", n.Relay, n.Key())
			}
			if _, err := f.a.core.Store.DropRelay(n.Relay.Seq); err != nil {
				t.Fatal(err)
			}
			if b := f.a.board(t); slices.ContainsFunc(b.Needs, func(n Need) bool { return n.Relay != nil }) {
				t.Errorf("owner queue still shows the relay after dropping it")
			}
		})
	}
}

func TestARefusedRelayLeavesTheQueueWhenItsHostCanActAgain(t *testing.T) {
	f := newFleet(t)
	f.c.core.RunSelf = func(...string) (string, error) { return "", errors.New("database is locked") }
	if err := f.a.core.Relay("c", RelayYM, relayYM{Args: []string{"item", "answer", "#3", "yes"}}); err != nil {
		t.Fatal(err)
	}
	for range RefusalsBeforeOwner {
		f.sync(t)
	}
	isRelay := func(n Need) bool { return n.Relay != nil }
	if !slices.ContainsFunc(f.a.board(t).Needs, isRelay) {
		t.Fatal("the refused command never reached the owner")
	}
	f.c.core.RunSelf = func(...string) (string, error) { return "answered", nil }
	f.sync(t)
	if slices.ContainsFunc(f.a.board(t).Needs, isRelay) {
		t.Fatal("the delivered command still needs the owner")
	}
}

func TestAnotherHostsGoalShowsTheBriefThatHostKeeps(t *testing.T) {
	at := newOrgWorld(t).clock
	brief := &Brief{Manager: "mgr1", Version: 3, Assessment: "caesar chain is the long pole", CreatedAt: at}
	for _, c := range []struct {
		name  string
		fresh map[string]Freshness
		want  string
	}{
		{"fresh brief there", map[string]Freshness{"mgr1": {Brief: brief}}, "brief v3"},
		{"brief behind there", map[string]Freshness{"mgr1": {Brief: brief, Behind: at, BehindWhat: "a merge"}}, "brief v3 stale"},
		{"none written there", nil, "no brief"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newOrgWorld(t)
			w.core.Remotes = &fakeRemotes{boards: []RemoteBoard{{Host: "box", At: w.clock, Self: "box",
				Managers: []Manager{{ID: "mgr1", Role: RoleManager, Name: "ship it", GoalID: "goal1"}},
				Goals:    []Goal{{ID: "goal1", Ask: "ship it", ManagerID: "mgr1", State: GoalActive, LastMovedAt: w.clock}},
				Fresh:    c.fresh}}}
			w.core.ShowRemotes = true
			n, ok := w.board(t).Node("box/mgr1")
			if !ok || !strings.HasPrefix(n.Fresh.String(), c.want) {
				t.Errorf("box/mgr1 brief %q, want it to start %q", n.Fresh.String(), c.want)
			}
		})
	}
}

func TestBriefLinksNameTheirHostOnceAnotherHostCarriesIt(t *testing.T) {
	w := newOrgWorld(t)
	if got := w.core.BriefURL("mgr1"); got != BriefURL("mgr1") {
		t.Errorf("uncarried host links %q, want %q", got, BriefURL("mgr1"))
	}
	if err := w.core.Store.setRelaySelf(RelaySelf{Name: "box.example"}); err != nil {
		t.Fatal(err)
	}
	if got := w.core.BriefURL("mgr1"); got != BriefURL("box.example/mgr1") {
		t.Errorf("carried host links %q, want %q", got, BriefURL("box.example/mgr1"))
	}
}

// silentRemotes reach hosts whose latest read went unanswered.
type silentRemotes struct{ fakeRemotes }

func (s *silentRemotes) Silent(string) error {
	return fmt.Errorf("%w: no answer for 2m", ErrUnreachable)
}

func TestAQueuedKillLandsOnceWhetherItsTargetLivesEndedOrWasKilledTwice(t *testing.T) {
	for _, tc := range []struct {
		name  string
		kills int
		ran   error
	}{
		{"live target", 1, nil},
		{"target already ended", 1, fmt.Errorf("ym kill --yes s1: %w: session \"s1\": %w", ErrGone, ErrNotFound)},
		{"killed twice before the host answers", 2, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			f.a.core.Remotes = &silentRemotes{fakeRemotes{boards: []RemoteBoard{{Host: "c"}}}}
			var ran [][]string
			f.c.core.RunSelf = func(args ...string) (string, error) {
				ran = append(ran, args)
				return "killed claude s1", tc.ran
			}
			for range tc.kills {
				if msg, err := f.a.core.RunOrRelay("c", "kill", "--yes", "s1"); err != nil || !strings.HasPrefix(msg, "queued for c") {
					t.Fatalf("RunOrRelay = %q, %v", msg, err)
				}
			}
			if pending, _ := f.a.core.Store.PendingRelays(); len(pending) != 1 {
				t.Fatalf("%d relays queued for %d kills of one target, want 1", len(pending), tc.kills)
			}
			for range RefusalsBeforeOwner {
				f.sync(t)
			}
			if pending, _ := f.a.core.Store.PendingRelays(); len(pending) != 0 {
				t.Errorf("still pending after the host answered: %+v", pending)
			}
			if len(ran) != 1 || strings.Join(ran[0], " ") != "kill --yes s1" {
				t.Errorf("c ran %v, want one kill --yes s1", ran)
			}
		})
	}
}

func (n *hostNet) StartWork(from, host string, w RemoteAssignment) error {
	if n.down[host] {
		return ErrUnreachable
	}
	_, err := n.hosts[host].core.ServeSync(RelaySync{From: from, As: host, Start: &w})
	return err
}

func (n *hostNet) AuthorizeWork(origin, id string, run func(RemoteAssignment) error) error {
	if n.down[origin] {
		return ErrUnreachable
	}
	return n.hosts[origin].core.AuthorizeWork(id, run)
}

func TestARelaunchQueuedOnAHostOthersPullFromRunsOnTheHostItNames(t *testing.T) {
	for _, tc := range []struct {
		name string
		// dest is where the manager lives: a pulls b's relays, and c is a host only a reaches.
		dest    string
		refuses int
	}{
		{name: "the host that pulls", dest: "a"},
		{name: "the host that pulls, after its first run fails", dest: "a", refuses: 1},
		{name: "a host the puller reaches", dest: "c"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			var ran []string
			refuses := tc.refuses
			map[string]*orgWorld{"a": f.a, "c": f.c}[tc.dest].core.RunSelf = func(args ...string) (string, error) {
				if refuses > 0 {
					refuses--
					return "", errors.New("ym goal relaunch mgr54: git ls-remote --heads origin: exit status 128: Connection closed")
				}
				ran = append(ran, strings.Join(args, " "))
				return "", nil
			}
			m, err := f.b.core.ReplaceManager(Board{}, RemoteKey(tc.dest, "mgr54"))
			if err != nil || m.Host != tc.dest {
				t.Fatalf("ReplaceManager = %+v, %v; want it queued for %s", m, err, tc.dest)
			}
			for range 2 + tc.refuses {
				f.a.core.SyncRelays([]string{"b", "c"}, f.net)
			}
			if len(ran) != 1 || ran[0] != "goal relaunch mgr54" {
				t.Errorf("%s ran %q, want b's relaunch run there once", tc.dest, ran)
			}
			if pending, _ := f.b.core.Store.PendingRelays(); len(pending) != 0 {
				t.Errorf("b still holds %+v after %s ran the relaunch", pending, tc.dest)
			}
		})
	}
}

func TestARelayedGoalCommandsOutcomeReachesTheChiefThatAskedWhole(t *testing.T) {
	long := " in yamnaya-wt-mgr54-yamnaya-seems-to-mark-2, " + strings.Repeat("with every word of why kept ", 10) + "to the end"
	for _, tc := range []struct {
		name string
		// fails is how many runs fail before one succeeds; final makes the first failure end the relay.
		fails int
		final bool
		want  []string
	}{
		{name: "it ran", want: []string{"relayed: ym goal relaunch MGR from b: MGR relaunched as 0badc0de" + long}},
		{name: "it failed, then ran", fails: 2, want: []string{
			"relay-failed: ym goal relaunch MGR from b failed: git ls-remote --heads origin: Connection closed" + long,
			"relayed: ym goal relaunch MGR from b: MGR relaunched as 0badc0de" + long}},
		{name: "it was refused for good", fails: 1, final: true, want: []string{
			"relay-failed: ym goal relaunch MGR from b refused: git ls-remote --heads origin: Connection closed" + long}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "platform")
			g, mgr := f.a.goal(t, "ship the fix", "/repo/app")
			if err := f.a.core.Attach(mgr.ID, RemoteKey("b", chief.ID)); err != nil {
				t.Fatal(err)
			}
			cycle := func() {
				t.Helper()
				if err := f.a.core.Loop(f.a.board(t)); err != nil {
					t.Fatal(err)
				}
				f.a.core.SyncRelays([]string{"b", "c"}, f.net)
				if err := f.b.core.Loop(f.b.board(t)); err != nil {
					t.Fatal(err)
				}
			}
			cycle()
			fails := tc.fails
			f.a.core.RunSelf = func(args ...string) (string, error) {
				if fails > 0 {
					fails--
					err := errors.New("ym " + strings.Join(args, " ") + ": git ls-remote --heads origin: Connection closed" + long)
					if tc.final {
						err = Final(err)
					}
					return "", err
				}
				return mgr.ID + " relaunched as 0badc0de" + long, nil
			}
			f.b.core.Requester = func() string { return chief.Session }
			if _, err := f.b.core.ReplaceManager(Board{}, RemoteKey("a", mgr.ID)); err != nil {
				t.Fatal(err)
			}
			for range 3 + tc.fails {
				cycle()
			}
			var heard []string
			for _, h := range f.b.handedTo(chief.Session) {
				for _, line := range strings.Split(h, "\n") {
					if _, kind, ok := strings.Cut(line, " #a/"); ok && strings.Contains(line, "a/"+g.ID) {
						_, rest, _ := strings.Cut(kind, " ")
						heard = append(heard, rest)
					}
				}
			}
			var want []string
			for _, w := range tc.want {
				want = append(want, strings.ReplaceAll(w, "MGR", mgr.ID))
			}
			if !slices.Equal(heard, want) {
				t.Errorf("chief heard %q, want %q", heard, want)
			}
			if items, _ := f.b.core.Store.Items(ItemFilter{}); len(items) != 0 {
				t.Errorf("b filed %+v; the goal's feed carries the outcome", items)
			}
		})
	}
}

func TestARelayedCommandThatNamesNoGoalTellsItsRequesterHowItWent(t *testing.T) {
	for _, tc := range []struct {
		name string
		// dest pulls b's relays, or is a host only the puller reaches.
		dest  string
		fails int
		final bool
		// told is what the requester hears directly; asked is the item it gets, "" for none.
		told, asked string
	}{
		{name: "ran on the host that pulls", dest: "a", told: "msg: ym: ym item answer #3 yes on a: answered #3"},
		{name: "ran on a host the puller reaches", dest: "c", told: "msg: ym: ym item answer #3 yes on c: answered #3"},
		{name: "failed twice, then ran", dest: "a", fails: 2,
			told: "msg: ym: ym item answer #3 yes on a: answered #3", asked: "ym item answer #3 yes on a failed: database is locked"},
		{name: "refused for good past the puller", dest: "c", fails: 1, final: true,
			asked: "ym item answer #3 yes on c was refused for good: database is locked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFleet(t)
			chief := f.b.chief(t, "platform")
			fails := tc.fails
			map[string]*orgWorld{"a": f.a, "c": f.c}[tc.dest].core.RunSelf = func(args ...string) (string, error) {
				if fails > 0 {
					fails--
					err := errors.New("ym " + strings.Join(args, " ") + ": database is locked")
					if tc.final {
						err = Final(err)
					}
					return "", err
				}
				return "answered #3", nil
			}
			f.b.core.Requester = func() string { return chief.Session }
			if _, err := f.b.core.RunOrRelay(tc.dest, "item", "answer", "#3", "yes"); err != nil {
				t.Fatal(err)
			}
			for range 4 + tc.fails {
				f.a.core.SyncRelays([]string{"b", "c"}, f.net)
			}
			var told []string
			for _, h := range f.b.handedTo(chief.Session) {
				if strings.Contains(h, " on "+tc.dest+": ") {
					told = append(told, h)
				}
			}
			if want := []string{tc.told}; tc.told == "" && len(told) != 0 || tc.told != "" && !slices.Equal(told, want) {
				t.Errorf("requester was told %q, want %q", told, tc.told)
			}
			items, err := f.b.core.Store.Items(ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			var asked []string
			for _, it := range items {
				asked = append(asked, it.Question)
			}
			if want := []string{tc.asked}; tc.asked == "" && len(asked) != 0 || tc.asked != "" && !slices.Equal(asked, want) {
				t.Errorf("requester's items %q, want one: %q", asked, tc.asked)
			}
			if tc.asked != "" && len(items) == 1 && items[0].Recipient != chief.ID {
				t.Errorf("item went to %q, want the chief that asked, %s", items[0].Recipient, chief.ID)
			}
		})
	}
}
